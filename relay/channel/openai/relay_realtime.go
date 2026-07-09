package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func OpenaiRealtimeHandler(c *gin.Context, info *relaycommon.RelayInfo) (*types.NewAPIError, *dto.RealtimeUsage) {
	if info == nil || info.ClientWs == nil || info.TargetWs == nil {
		return types.NewError(fmt.Errorf("invalid websocket connection"), types.ErrorCodeBadResponse), nil
	}

	info.IsStream = true
	clientConn := info.ClientWs
	targetConn := info.TargetWs

	if info.StreamStatus == nil {
		info.StreamStatus = relaycommon.NewStreamStatus()
	}
	attemptCtx, cancelAttempt := context.WithCancel(c.Request.Context())
	defer cancelAttempt()

	clientClosed := make(chan struct{})
	targetClosed := make(chan struct{})
	sendChan := make(chan []byte, 100)
	receiveChan := make(chan []byte, 100)
	type realtimeRelayError struct {
		source string
		err    error
	}
	errChan := make(chan realtimeRelayError, 2)
	firstTargetMessage := make(chan struct{}, 1)
	noUpstreamResponse := func() bool {
		return info.SendResponseCount == 0 && info.ReceivedResponseCount == 0 && !info.HasSendResponse()
	}

	usage := &dto.RealtimeUsage{}
	localUsage := &dto.RealtimeUsage{}
	sumUsage := &dto.RealtimeUsage{}
	var (
		wg           sync.WaitGroup
		cleanupOnce  sync.Once
		replayMu     sync.Mutex
		replayBuffer = cloneRealtimeMessages(info.RealtimeReplayMessages)
	)

	cleanupAttempt := func() {
		cleanupOnce.Do(func() {
			cancelAttempt()
			_ = targetConn.Close()
			_ = clientConn.SetReadDeadline(time.Now())
			wg.Wait()
			_ = clientConn.SetReadDeadline(time.Time{})
		})
	}
	defer cleanupAttempt()

	for _, message := range replayBuffer {
		if err := handleRealtimeClientMessage(c, info, targetConn, message, localUsage); err != nil {
			return types.NewError(err, types.ErrorCodeDoRequestFailed), nil
		}
	}

	wg.Add(1)
	gopool.Go(func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				errChan <- realtimeRelayError{source: "client", err: fmt.Errorf("panic in client reader: %v", r)}
			}
		}()
		for {
			select {
			case <-attemptCtx.Done():
				return
			default:
				_, message, err := clientConn.ReadMessage()
				if err != nil {
					select {
					case <-attemptCtx.Done():
						close(clientClosed)
						return
					default:
					}
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						errChan <- realtimeRelayError{source: "client", err: fmt.Errorf("error reading from client: %v", err)}
					}
					close(clientClosed)
					return
				}
				select {
				case <-attemptCtx.Done():
					return
				default:
				}

				if err := handleRealtimeClientMessage(c, info, targetConn, message, localUsage); err != nil {
					errChan <- realtimeRelayError{source: "client", err: err}
					return
				}
				replayMu.Lock()
				replayBuffer = append(replayBuffer, append([]byte(nil), message...))
				replayMu.Unlock()

				select {
				case sendChan <- message:
				default:
				}
			}
		}
	})

	wg.Add(1)
	gopool.Go(func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				errChan <- realtimeRelayError{source: "target", err: fmt.Errorf("panic in target reader: %v", r)}
			}
		}()
		for {
			select {
			case <-attemptCtx.Done():
				return
			default:
				_, message, err := targetConn.ReadMessage()
				if err != nil {
					select {
					case <-attemptCtx.Done():
						close(targetClosed)
						return
					default:
					}
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						errChan <- realtimeRelayError{source: "target", err: fmt.Errorf("error reading from target: %v", err)}
					}
					close(targetClosed)
					return
				}
				select {
				case <-attemptCtx.Done():
					return
				default:
				}
				info.SetFirstResponseTime()
				info.ReceivedResponseCount++
				select {
				case firstTargetMessage <- struct{}{}:
				default:
				}
				realtimeEvent := &dto.RealtimeEvent{}
				err = common.Unmarshal(message, realtimeEvent)
				if err != nil {
					errChan <- realtimeRelayError{source: "target", err: fmt.Errorf("error unmarshalling message: %v", err)}
					return
				}

				if realtimeEvent.Type == dto.RealtimeEventTypeResponseDone {
					realtimeUsage := realtimeEvent.Response.Usage
					if realtimeUsage != nil {
						usage.TotalTokens += realtimeUsage.TotalTokens
						usage.InputTokens += realtimeUsage.InputTokens
						usage.OutputTokens += realtimeUsage.OutputTokens
						usage.InputTokenDetails.AudioTokens += realtimeUsage.InputTokenDetails.AudioTokens
						usage.InputTokenDetails.CachedTokens += realtimeUsage.InputTokenDetails.CachedTokens
						usage.InputTokenDetails.TextTokens += realtimeUsage.InputTokenDetails.TextTokens
						usage.OutputTokenDetails.AudioTokens += realtimeUsage.OutputTokenDetails.AudioTokens
						usage.OutputTokenDetails.TextTokens += realtimeUsage.OutputTokenDetails.TextTokens
						err := preConsumeUsage(c, info, usage, sumUsage)
						if err != nil {
							errChan <- realtimeRelayError{source: "target", err: fmt.Errorf("error consume usage: %v", err)}
							return
						}
						// 本次计费完成，清除
						usage = &dto.RealtimeUsage{}

						localUsage = &dto.RealtimeUsage{}
					} else {
						textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
						if err != nil {
							errChan <- realtimeRelayError{source: "target", err: fmt.Errorf("error counting text token: %v", err)}
							return
						}
						logger.LogInfo(c, fmt.Sprintf("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
						localUsage.TotalTokens += textToken + audioToken
						info.IsFirstRequest = false
						localUsage.InputTokens += textToken + audioToken
						localUsage.InputTokenDetails.TextTokens += textToken
						localUsage.InputTokenDetails.AudioTokens += audioToken
						err = preConsumeUsage(c, info, localUsage, sumUsage)
						if err != nil {
							errChan <- realtimeRelayError{source: "target", err: fmt.Errorf("error consume usage: %v", err)}
							return
						}
						// 本次计费完成，清除
						localUsage = &dto.RealtimeUsage{}
						// print now usage
					}
					logger.LogInfo(c, fmt.Sprintf("realtime streaming sumUsage: %v", sumUsage))
					logger.LogInfo(c, fmt.Sprintf("realtime streaming localUsage: %v", localUsage))
					logger.LogInfo(c, fmt.Sprintf("realtime streaming localUsage: %v", localUsage))

				} else if realtimeEvent.Type == dto.RealtimeEventTypeSessionUpdated || realtimeEvent.Type == dto.RealtimeEventTypeSessionCreated {
					realtimeSession := realtimeEvent.Session
					if realtimeSession != nil {
						// update audio format
						info.InputAudioFormat = common.GetStringIfEmpty(realtimeSession.InputAudioFormat, info.InputAudioFormat)
						info.OutputAudioFormat = common.GetStringIfEmpty(realtimeSession.OutputAudioFormat, info.OutputAudioFormat)
					}
				} else {
					textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
					if err != nil {
						errChan <- realtimeRelayError{source: "target", err: fmt.Errorf("error counting text token: %v", err)}
						return
					}
					logger.LogInfo(c, fmt.Sprintf("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
					localUsage.TotalTokens += textToken + audioToken
					localUsage.OutputTokens += textToken + audioToken
					localUsage.OutputTokenDetails.TextTokens += textToken
					localUsage.OutputTokenDetails.AudioTokens += audioToken
				}

				err = helper.WssString(c, clientConn, string(message))
				if err != nil {
					errChan <- realtimeRelayError{source: "target", err: fmt.Errorf("error writing to client: %v", err)}
					return
				}
				info.SendResponseCount++

				select {
				case receiveChan <- message:
				default:
				}
			}
		}
	})

	var handlerErr *types.NewAPIError
	firstByteTimeout := helper.FirstByteFailoverTimeout(info)
	var firstByteTimer *time.Timer
	var firstByteC <-chan time.Time
	if firstByteTimeout > 0 {
		firstByteTimer = time.NewTimer(firstByteTimeout)
		firstByteC = firstByteTimer.C
		defer firstByteTimer.Stop()
	}

waitLoop:
	for {
		select {
		case <-clientClosed:
			break waitLoop
		case <-targetClosed:
			if noUpstreamResponse() {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, nil)
				handlerErr = types.NewErrorWithStatusCode(errors.New("upstream realtime closed before first response"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
			}
			break waitLoop
		case relayErr := <-errChan:
			//return service.OpenAIErrorWrapper(err, "realtime_error", http.StatusInternalServerError), nil
			logger.LogError(c, "realtime error: "+relayErr.err.Error())
			if relayErr.source == "target" && noUpstreamResponse() {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, relayErr.err)
				handlerErr = types.NewErrorWithStatusCode(errors.New("upstream realtime failed before first response"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
			}
			break waitLoop
		case <-firstTargetMessage:
			if firstByteTimer != nil {
				firstByteTimer.Stop()
			}
			firstByteC = nil
		case <-firstByteC:
			if noUpstreamResponse() {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, nil)
				handlerErr = types.NewErrorWithStatusCode(errors.New("upstream first byte timeout"), types.ErrorCodeBadResponseStatusCode, http.StatusGatewayTimeout)
			}
			break waitLoop
		case <-attemptCtx.Done():
			break waitLoop
		}
	}

	cleanupAttempt()
	if handlerErr != nil {
		replayMu.Lock()
		info.RealtimeReplayMessages = cloneRealtimeMessages(replayBuffer)
		replayMu.Unlock()
		return handlerErr, sumUsage
	}
	info.RealtimeReplayMessages = nil

	if usage.TotalTokens != 0 {
		_ = preConsumeUsage(c, info, usage, sumUsage)
	}

	if localUsage.TotalTokens != 0 {
		_ = preConsumeUsage(c, info, localUsage, sumUsage)
	}

	// check usage total tokens, if 0, use local usage

	return nil, sumUsage
}

func handleRealtimeClientMessage(c *gin.Context, info *relaycommon.RelayInfo, targetConn *websocket.Conn, message []byte, usage *dto.RealtimeUsage) error {
	realtimeEvent := &dto.RealtimeEvent{}
	if err := common.Unmarshal(message, realtimeEvent); err != nil {
		return fmt.Errorf("error unmarshalling message: %v", err)
	}

	if realtimeEvent.Type == dto.RealtimeEventTypeSessionUpdate && realtimeEvent.Session != nil && realtimeEvent.Session.Tools != nil {
		info.RealtimeTools = realtimeEvent.Session.Tools
	}

	textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
	if err != nil {
		return fmt.Errorf("error counting text token: %v", err)
	}
	logger.LogInfo(c, fmt.Sprintf("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
	usage.TotalTokens += textToken + audioToken
	usage.InputTokens += textToken + audioToken
	usage.InputTokenDetails.TextTokens += textToken
	usage.InputTokenDetails.AudioTokens += audioToken

	if err := helper.WssString(c, targetConn, string(message)); err != nil {
		return fmt.Errorf("error writing to target: %v", err)
	}
	return nil
}

func cloneRealtimeMessages(messages [][]byte) [][]byte {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([][]byte, 0, len(messages))
	for _, message := range messages {
		cloned = append(cloned, append([]byte(nil), message...))
	}
	return cloned
}

func preConsumeUsage(ctx *gin.Context, info *relaycommon.RelayInfo, usage *dto.RealtimeUsage, totalUsage *dto.RealtimeUsage) error {
	if usage == nil || totalUsage == nil {
		return fmt.Errorf("invalid usage pointer")
	}

	totalUsage.TotalTokens += usage.TotalTokens
	totalUsage.InputTokens += usage.InputTokens
	totalUsage.OutputTokens += usage.OutputTokens
	totalUsage.InputTokenDetails.CachedTokens += usage.InputTokenDetails.CachedTokens
	totalUsage.InputTokenDetails.TextTokens += usage.InputTokenDetails.TextTokens
	totalUsage.InputTokenDetails.AudioTokens += usage.InputTokenDetails.AudioTokens
	totalUsage.OutputTokenDetails.TextTokens += usage.OutputTokenDetails.TextTokens
	totalUsage.OutputTokenDetails.AudioTokens += usage.OutputTokenDetails.AudioTokens
	// clear usage
	err := service.PreWssConsumeQuota(ctx, info, totalUsage)
	return err
}
