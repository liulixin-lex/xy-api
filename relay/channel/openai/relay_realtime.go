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

type realtimeRelayError struct {
	endReason relaycommon.StreamEndReason
	err       error
	apiErr    *types.NewAPIError
}

var errRealtimeFirstByteTimedOut = errors.New("realtime first byte timeout won before forwarding")

type realtimeFirstByteState struct {
	mu          sync.Mutex
	committed   bool
	writeFailed bool
	timedOut    bool
}

func (s *realtimeFirstByteState) forward(write func() error, commit func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.timedOut {
		return errRealtimeFirstByteTimedOut
	}
	if err := write(); err != nil {
		s.writeFailed = true
		return err
	}

	s.committed = true
	commit()
	return nil
}

func (s *realtimeFirstByteState) tryTimeout() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.committed || s.writeFailed || s.timedOut {
		return false
	}
	s.timedOut = true
	return true
}

func (s *realtimeFirstByteState) hasCommitted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.committed
}

func (e *realtimeRelayError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *realtimeRelayError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

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
	errChan := make(chan realtimeRelayError, 2)
	firstTargetMessage := make(chan struct{}, 1)
	firstByteState := &realtimeFirstByteState{
		committed: info.SendResponseCount > 0 || info.ReceivedResponseCount > 0 || info.HasSendResponse(),
	}
	noUpstreamResponse := func() bool {
		return !firstByteState.hasCommitted()
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
		if relayErr := handleRealtimeClientMessage(c, info, targetConn, message, localUsage); relayErr != nil {
			if relayErr.endReason != relaycommon.StreamEndReasonNone {
				info.StreamStatus.SetEndReason(relayErr.endReason, relayErr.err)
			}
			if relayErr.apiErr != nil {
				return relayErr.apiErr, nil
			}
			return types.NewErrorWithStatusCode(
				relayErr.err,
				types.ErrorCodeDoRequestFailed,
				http.StatusBadGateway,
				types.ErrOptionWithHideErrMsg("upstream realtime request failed"),
			), nil
		}
	}

	wg.Add(1)
	gopool.Go(func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				errChan <- realtimeRelayError{endReason: relaycommon.StreamEndReasonClientGone, err: fmt.Errorf("panic in client reader: %v", r)}
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
						return
					default:
					}
					if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						close(clientClosed)
					} else {
						errChan <- realtimeRelayError{endReason: relaycommon.StreamEndReasonClientGone, err: fmt.Errorf("error reading from client: %w", err)}
					}
					return
				}
				select {
				case <-attemptCtx.Done():
					return
				default:
				}

				if relayErr := handleRealtimeClientMessage(c, info, targetConn, message, localUsage); relayErr != nil {
					errChan <- *relayErr
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
				errChan <- realtimeRelayError{endReason: relaycommon.StreamEndReasonScannerErr, err: fmt.Errorf("panic in target reader: %v", r)}
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
						return
					default:
					}
					if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						close(targetClosed)
					} else {
						errChan <- realtimeRelayError{endReason: relaycommon.StreamEndReasonScannerErr, err: fmt.Errorf("error reading from target: %w", err)}
					}
					return
				}
				select {
				case <-attemptCtx.Done():
					return
				default:
				}
				realtimeEvent := &dto.RealtimeEvent{}
				err = common.Unmarshal(message, realtimeEvent)
				if err != nil {
					errChan <- realtimeRelayError{endReason: relaycommon.StreamEndReasonScannerErr, err: fmt.Errorf("error unmarshalling message: %w", err)}
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
						usage = &dto.RealtimeUsage{}
						localUsage = &dto.RealtimeUsage{}
						if err != nil {
							relayErr := fmt.Errorf("error consume usage: %w", err)
							errChan <- realtimeRelayError{
								err: relayErr,
								apiErr: types.NewError(
									relayErr,
									types.ErrorCodePreConsumeTokenQuotaFailed,
									types.ErrOptionWithHideErrMsg("failed to consume realtime quota"),
								),
							}
							return
						}
					} else {
						textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
						if err != nil {
							relayErr := fmt.Errorf("error counting realtime token: %w", err)
							errChan <- realtimeRelayError{
								err: relayErr,
								apiErr: types.NewError(
									relayErr,
									types.ErrorCodeCountTokenFailed,
									types.ErrOptionWithHideErrMsg("failed to count realtime tokens"),
								),
							}
							return
						}
						logger.LogInfo(c, fmt.Sprintf("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
						localUsage.TotalTokens += textToken + audioToken
						info.IsFirstRequest = false
						localUsage.InputTokens += textToken + audioToken
						localUsage.InputTokenDetails.TextTokens += textToken
						localUsage.InputTokenDetails.AudioTokens += audioToken
						err = preConsumeUsage(c, info, localUsage, sumUsage)
						localUsage = &dto.RealtimeUsage{}
						if err != nil {
							relayErr := fmt.Errorf("error consume usage: %w", err)
							errChan <- realtimeRelayError{
								err: relayErr,
								apiErr: types.NewError(
									relayErr,
									types.ErrorCodePreConsumeTokenQuotaFailed,
									types.ErrOptionWithHideErrMsg("failed to consume realtime quota"),
								),
							}
							return
						}
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
						relayErr := fmt.Errorf("error counting realtime token: %w", err)
						errChan <- realtimeRelayError{
							err: relayErr,
							apiErr: types.NewError(
								relayErr,
								types.ErrorCodeCountTokenFailed,
								types.ErrOptionWithHideErrMsg("failed to count realtime tokens"),
							),
						}
						return
					}
					logger.LogInfo(c, fmt.Sprintf("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
					localUsage.TotalTokens += textToken + audioToken
					localUsage.OutputTokens += textToken + audioToken
					localUsage.OutputTokenDetails.TextTokens += textToken
					localUsage.OutputTokenDetails.AudioTokens += audioToken
				}

				err = firstByteState.forward(func() error {
					return helper.WssString(c, clientConn, string(message))
				}, func() {
					info.SetFirstResponseTime()
					info.ReceivedResponseCount++
					info.SendResponseCount++
					select {
					case firstTargetMessage <- struct{}{}:
					default:
					}
				})
				if err != nil {
					if errors.Is(err, errRealtimeFirstByteTimedOut) {
						return
					}
					errChan <- realtimeRelayError{endReason: relaycommon.StreamEndReasonClientGone, err: fmt.Errorf("error writing to client: %w", err)}
					return
				}

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
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, nil)
			break waitLoop
		case <-targetClosed:
			if noUpstreamResponse() {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, nil)
				handlerErr = types.NewErrorWithStatusCode(errors.New("upstream realtime closed before first response"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
			} else {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
			}
			break waitLoop
		case relayErr := <-errChan:
			//return service.OpenAIErrorWrapper(err, "realtime_error", http.StatusInternalServerError), nil
			logger.LogError(c, "realtime error: "+relayErr.err.Error())
			if relayErr.endReason != relaycommon.StreamEndReasonNone {
				info.StreamStatus.SetEndReason(relayErr.endReason, relayErr.err)
			}
			if relayErr.apiErr != nil {
				handlerErr = relayErr.apiErr
			} else if relayErr.endReason == relaycommon.StreamEndReasonScannerErr && noUpstreamResponse() {
				handlerErr = types.NewErrorWithStatusCode(
					relayErr.err,
					types.ErrorCodeBadResponseStatusCode,
					http.StatusBadGateway,
					types.ErrOptionWithHideErrMsg("upstream realtime failed before first response"),
				)
			}
			break waitLoop
		case <-firstTargetMessage:
			if firstByteTimer != nil {
				firstByteTimer.Stop()
			}
			firstByteC = nil
		case <-firstByteC:
			firstByteC = nil
			if firstByteState.tryTimeout() {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, nil)
				handlerErr = types.NewErrorWithStatusCode(errors.New("upstream first byte timeout"), types.ErrorCodeBadResponseStatusCode, http.StatusGatewayTimeout)
				break waitLoop
			}
		case <-attemptCtx.Done():
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, attemptCtx.Err())
			break waitLoop
		}
	}

	cleanupAttempt()
	if handlerErr == nil || firstByteState.hasCommitted() {
		if usage.TotalTokens != 0 {
			_ = preConsumeUsage(c, info, usage, sumUsage)
		}

		if localUsage.TotalTokens != 0 {
			_ = preConsumeUsage(c, info, localUsage, sumUsage)
		}
	}
	if handlerErr != nil {
		replayMu.Lock()
		info.RealtimeReplayMessages = cloneRealtimeMessages(replayBuffer)
		replayMu.Unlock()
		return handlerErr, sumUsage
	}
	info.RealtimeReplayMessages = nil

	// check usage total tokens, if 0, use local usage

	return nil, sumUsage
}

func handleRealtimeClientMessage(c *gin.Context, info *relaycommon.RelayInfo, targetConn *websocket.Conn, message []byte, usage *dto.RealtimeUsage) *realtimeRelayError {
	realtimeEvent := &dto.RealtimeEvent{}
	if err := common.Unmarshal(message, realtimeEvent); err != nil {
		return &realtimeRelayError{
			endReason: relaycommon.StreamEndReasonClientGone,
			err:       fmt.Errorf("error unmarshalling client message: %w", err),
		}
	}

	if realtimeEvent.Type == dto.RealtimeEventTypeSessionUpdate && realtimeEvent.Session != nil && realtimeEvent.Session.Tools != nil {
		info.RealtimeTools = realtimeEvent.Session.Tools
	}

	textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
	if err != nil {
		relayErr := fmt.Errorf("error counting realtime token: %w", err)
		return &realtimeRelayError{
			err: relayErr,
			apiErr: types.NewError(
				relayErr,
				types.ErrorCodeCountTokenFailed,
				types.ErrOptionWithHideErrMsg("failed to count realtime tokens"),
			),
		}
	}
	logger.LogInfo(c, fmt.Sprintf("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
	usage.TotalTokens += textToken + audioToken
	usage.InputTokens += textToken + audioToken
	usage.InputTokenDetails.TextTokens += textToken
	usage.InputTokenDetails.AudioTokens += audioToken

	if err := helper.WssString(c, targetConn, string(message)); err != nil {
		return &realtimeRelayError{
			endReason: relaycommon.StreamEndReasonScannerErr,
			err:       fmt.Errorf("error writing to target: %w", err),
		}
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
