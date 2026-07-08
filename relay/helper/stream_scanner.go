package helper

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/bytedance/gopkg/util/gopool"

	"github.com/gin-gonic/gin"
)

const (
	InitialScannerBufferSize    = 64 << 10  // 64KB (64*1024)
	DefaultMaxScannerBufferSize = 128 << 20 // 64MB (64*1024*1024) default SSE buffer size
	DefaultPingInterval         = 10 * time.Second
	// streamWriteTimeout bounds a single blocked write to a slow client so the
	// unconditional wg.Wait() in cleanup can always finish. Without it, a slow
	// but connected client (full TCP buffer, no server WriteTimeout) could hang
	// the handler forever.
	streamWriteTimeout = 30 * time.Second
)

func getScannerBufferSize() int {
	if constant.StreamScannerMaxBufferMB > 0 {
		return constant.StreamScannerMaxBufferMB << 20
	}
	return DefaultMaxScannerBufferSize
}

func NewStreamScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, InitialScannerBufferSize), getScannerBufferSize())
	return scanner
}

// ExtendWriteDeadline pushes the connection write deadline forward before each
// stream write. Best-effort: writers that don't support deadlines (e.g.
// httptest recorders) are silently ignored.
func ExtendWriteDeadline(c *gin.Context) {
	if c == nil || c.Writer == nil {
		return
	}
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(streamWriteTimeout))
}

func StreamScannerHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, dataHandler func(data string, sr *StreamResult)) {

	if resp == nil || dataHandler == nil {
		return
	}

	// 无条件新建 StreamStatus
	info.StreamStatus = relaycommon.NewStreamStatus()

	ctx, cancel := context.WithCancel(context.Background())

	streamingTimeout := time.Duration(constant.StreamingTimeout) * time.Second

	var (
		stopChan       = make(chan bool, 3) // 增加缓冲区避免阻塞
		scanner        = NewStreamScanner(resp.Body)
		ticker         = time.NewTicker(streamingTimeout)
		pingTicker     *time.Ticker
		firstByteTimer *time.Timer
		firstByteC     <-chan time.Time
		firstByteSeen  = make(chan struct{})
		firstByteOnce  sync.Once
		writeMutex     sync.Mutex     // Mutex to protect concurrent writes
		wg             sync.WaitGroup // 用于等待所有 goroutine 退出
		cleanupOnce    sync.Once
		stopOnce       sync.Once
	)

	stop := func() {
		stopOnce.Do(func() {
			close(stopChan)
		})
	}

	generalSettings := operation_setting.GetGeneralSetting()
	pingEnabled := generalSettings.PingIntervalEnabled && !info.DisablePing
	pingInterval := time.Duration(generalSettings.PingIntervalSeconds) * time.Second
	if pingInterval <= 0 {
		pingInterval = DefaultPingInterval
	}

	if pingEnabled {
		pingTicker = time.NewTicker(pingInterval)
	}
	firstByteTimeout := firstByteFailoverTimeout(info)
	if firstByteTimeout > 0 {
		firstByteTimer = time.NewTimer(firstByteTimeout)
		firstByteC = firstByteTimer.C
	}
	markFirstByteSeen := func() {
		firstByteOnce.Do(func() {
			close(firstByteSeen)
		})
	}

	logger.LogDebug(c, "relay timeout seconds: %d", common.RelayTimeout)
	logger.LogDebug(c, "relay max idle conns: %d", common.RelayMaxIdleConns)
	logger.LogDebug(c, "relay max idle conns per host: %d", common.RelayMaxIdleConnsPerHost)
	logger.LogDebug(c, "streaming timeout seconds: %d", int64(streamingTimeout.Seconds()))
	logger.LogDebug(c, "ping interval seconds: %d", int64(pingInterval.Seconds()))

	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()
			stop()
			if resp.Body != nil {
				_ = resp.Body.Close()
			}

			ticker.Stop()
			if pingTicker != nil {
				pingTicker.Stop()
			}
			if firstByteTimer != nil {
				firstByteTimer.Stop()
			}

			wg.Wait()
		})
	}
	// Ensure gin.Context is not returned to Gin's pool while any stream goroutine can still use it.
	defer cleanup()

	scanner.Split(bufio.ScanLines)
	SetEventStreamHeaders(c)

	ctx = context.WithValue(ctx, "stop_chan", stopChan)

	// Handle ping data sending with improved error handling
	if pingEnabled && pingTicker != nil {
		wg.Add(1)
		gopool.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					logger.LogError(c, fmt.Sprintf("ping goroutine panic: %v", r))
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("ping panic: %v", r))
					stop()
				}
				logger.LogDebug(c, "ping goroutine exited")
				wg.Done()
			}()

			// 添加超时保护，防止 goroutine 无限运行
			maxPingDuration := 30 * time.Minute // 最大 ping 持续时间
			pingTimeout := time.NewTimer(maxPingDuration)
			defer pingTimeout.Stop()

			for {
				select {
				case <-pingTicker.C:
					if firstByteTimeout > 0 && !info.HasSendResponse() && info.ReceivedResponseCount == 0 {
						continue
					}
					var err error
					func() {
						writeMutex.Lock()
						defer writeMutex.Unlock()
						ExtendWriteDeadline(c)
						err = PingData(c)
					}()
					if err != nil {
						logger.LogError(c, "ping data error: "+err.Error())
						info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPingFail, err)
						return
					}
					logger.LogDebug(c, "ping data sent")
				case <-ctx.Done():
					return
				case <-stopChan:
					return
				case <-c.Request.Context().Done():
					// 监听客户端断开连接
					return
				case <-pingTimeout.C:
					logger.LogError(c, "ping goroutine max duration reached")
					return
				}
			}
		})
	}

	dataChan := make(chan string, 10)

	wg.Add(1)
	gopool.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				logger.LogError(c, fmt.Sprintf("data handler goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("handler panic: %v", r))
			}
			stop()
			wg.Done()
		}()
		sr := newStreamResult(info.StreamStatus)
		for data := range dataChan {
			sr.reset()
			func() {
				writeMutex.Lock()
				defer writeMutex.Unlock()
				ExtendWriteDeadline(c)
				dataHandler(data, sr)
			}()
			if sr.IsStopped() {
				return
			}
		}
	})

	// Scanner goroutine with improved error handling
	wg.Add(1)
	common.RelayCtxGo(ctx, func() {
		defer func() {
			close(dataChan)
			if r := recover(); r != nil {
				logger.LogError(c, fmt.Sprintf("scanner goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("scanner panic: %v", r))
			}
			stop()
			logger.LogDebug(c, "scanner goroutine exited")
			wg.Done()
		}()

		for scanner.Scan() {
			// 检查是否需要停止
			select {
			case <-stopChan:
				return
			case <-ctx.Done():
				return
			default:
			}

			ticker.Reset(streamingTimeout)
			data := scanner.Text()
			logger.LogDebug(c, "stream scanner data: %s", data)

			if len(data) < 6 {
				continue
			}
			if data[:5] != "data:" && data[:6] != "[DONE]" {
				continue
			}
			data = data[5:]
			data = strings.TrimSpace(data)
			if data == "" {
				continue
			}
			if !strings.HasPrefix(data, "[DONE]") {
				info.SetFirstResponseTime()
				info.ReceivedResponseCount++
				markFirstByteSeen()

				select {
				case dataChan <- data:
				case <-ctx.Done():
					return
				case <-stopChan:
					return
				}
			} else {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
				logger.LogDebug(c, "received [DONE], stopping scanner")
				return
			}
		}

		if err := scanner.Err(); err != nil {
			if err != io.EOF {
				logger.LogError(c, "scanner error: "+err.Error())
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, err)
			}
		}
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	})

	// 主循环等待完成或超时
waitLoop:
	for {
		select {
		case <-ticker.C:
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonTimeout, nil)
			break waitLoop
		case <-firstByteC:
			if !info.HasSendResponse() && info.SendResponseCount == 0 && info.ReceivedResponseCount == 0 {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, nil)
				break waitLoop
			}
		case <-firstByteSeen:
			if firstByteTimer != nil {
				firstByteTimer.Stop()
			}
			firstByteC = nil
			firstByteSeen = nil
		case <-stopChan:
			// EndReason already set by the goroutine that triggered stopChan
			break waitLoop
		case <-c.Request.Context().Done():
			// 客户端断开：立即 cleanup 关闭上游 resp.Body，解除 scanner 阻塞并让上游停止生成，
			// 避免为已放弃的请求继续消费上游 token。
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, c.Request.Context().Err())
			break waitLoop
		}
	}

	cleanup()
	if info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() {
		logger.LogInfo(c, fmt.Sprintf("stream ended: %s", info.StreamStatus.Summary()))
	} else {
		logger.LogError(c, fmt.Sprintf("stream ended: %s, received=%d", info.StreamStatus.Summary(), info.ReceivedResponseCount))
	}
}

func firstByteFailoverTimeout(info *relaycommon.RelayInfo) time.Duration {
	return FirstByteFailoverTimeout(info)
}

type FirstByteGuard struct {
	info      *relaycommon.RelayInfo
	closer    io.Closer
	done      chan struct{}
	timer     *time.Timer
	doneOnce  sync.Once
	firstOnce sync.Once
	wg        sync.WaitGroup
}

func NewFirstByteGuard(info *relaycommon.RelayInfo, closer io.Closer) *FirstByteGuard {
	guard := &FirstByteGuard{
		info:   info,
		closer: closer,
		done:   make(chan struct{}),
	}
	if info == nil {
		return guard
	}
	if info.StreamStatus == nil {
		info.StreamStatus = relaycommon.NewStreamStatus()
	}
	firstByteTimeout := FirstByteFailoverTimeout(info)
	if firstByteTimeout <= 0 {
		return guard
	}

	guard.timer = time.NewTimer(firstByteTimeout)
	guard.wg.Add(1)
	go func() {
		defer guard.wg.Done()
		select {
		case <-guard.timer.C:
			if guard.noResponse() {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonFirstByteTimeout, nil)
				if closer != nil {
					_ = closer.Close()
				}
			}
		case <-guard.done:
		}
	}()
	return guard
}

func (g *FirstByteGuard) MarkReceived() {
	if g == nil || g.info == nil {
		return
	}
	g.firstOnce.Do(func() {
		if g.TimedOutBeforeResponse() {
			return
		}
		g.info.SetFirstResponseTime()
		g.info.ReceivedResponseCount++
		g.stopWatch()
	})
}

func (g *FirstByteGuard) Stop() {
	if g == nil {
		return
	}
	g.stopWatch()
	g.wg.Wait()
}

func (g *FirstByteGuard) TimedOutBeforeResponse() bool {
	return g != nil && g.info != nil && g.info.FirstByteTimedOutBeforeResponse()
}

func (g *FirstByteGuard) noResponse() bool {
	return g != nil && g.info != nil && g.info.SendResponseCount == 0 && g.info.ReceivedResponseCount == 0 && !g.info.HasSendResponse()
}

func (g *FirstByteGuard) stopWatch() {
	if g == nil {
		return
	}
	if g.timer != nil {
		g.timer.Stop()
	}
	g.doneOnce.Do(func() {
		close(g.done)
	})
}

func FirstByteFailoverTimeout(info *relaycommon.RelayInfo) time.Duration {
	if info == nil {
		return 0
	}
	setting := smart_routing_setting.GetSetting()
	if !setting.Enabled || !setting.FirstByteFailoverEnabled {
		return 0
	}
	if setting.Mode != smart_routing_setting.ModeBalanced && setting.Mode != smart_routing_setting.ModeEnterpriseSLO {
		return 0
	}
	minMs := setting.FirstByteMinMs
	if minMs <= 0 {
		return 0
	}
	capMs := setting.FirstByteCapMs
	if capMs < minMs {
		capMs = minMs
	}
	timeoutMs := minMs
	if info.ChannelMeta != nil && info.ChannelId > 0 && info.OriginModelName != "" {
		group := info.UsingGroup
		if group == "" {
			group = "default"
		}
		if metric, ok := routinghotcache.GetMetric(routinghotcache.Key{
			ChannelID:   info.ChannelId,
			APIKeyIndex: model.RoutingMetricSingleKeyIndex,
			Model:       info.OriginModelName,
			Group:       group,
		}); ok && metric.P95LatencyMs > 0 && setting.FirstByteP95Multiplier > 0 {
			derived := int(metric.P95LatencyMs * setting.FirstByteP95Multiplier)
			if derived > timeoutMs {
				timeoutMs = derived
			}
		}
	}
	if timeoutMs > capMs {
		timeoutMs = capMs
	}
	return time.Duration(timeoutMs) * time.Millisecond
}
