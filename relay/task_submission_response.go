package relay

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const maxTaskSubmissionResponseBytes int64 = 3 << 20

var errTaskSubmissionResponseTooLarge = errors.New("task submission response is too large")

type taskSubmissionResponseWriter struct {
	gin.ResponseWriter
	header   http.Header
	body     bytes.Buffer
	status   int
	size     int
	limit    int64
	overflow bool
}

func newTaskSubmissionResponseWriter(writer gin.ResponseWriter, limit int64) *taskSubmissionResponseWriter {
	return &taskSubmissionResponseWriter{
		ResponseWriter: writer,
		header:         make(http.Header),
		size:           -1,
		limit:          limit,
	}
}

func (writer *taskSubmissionResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *taskSubmissionResponseWriter) WriteHeader(status int) {
	if status > 0 && writer.size < 0 {
		writer.status = status
	}
}

func (writer *taskSubmissionResponseWriter) WriteHeaderNow() {
	if writer.size >= 0 {
		return
	}
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	writer.size = 0
}

func (writer *taskSubmissionResponseWriter) Write(data []byte) (int, error) {
	writer.WriteHeaderNow()
	if writer.overflow || writer.limit <= 0 || int64(writer.body.Len())+int64(len(data)) > writer.limit {
		writer.overflow = true
		return 0, errTaskSubmissionResponseTooLarge
	}
	written, err := writer.body.Write(data)
	writer.size += written
	return written, err
}

func (writer *taskSubmissionResponseWriter) WriteString(data string) (int, error) {
	return writer.Write([]byte(data))
}

func (writer *taskSubmissionResponseWriter) Status() int {
	if writer.status == 0 {
		return http.StatusOK
	}
	return writer.status
}

func (writer *taskSubmissionResponseWriter) Size() int {
	return writer.size
}

func (writer *taskSubmissionResponseWriter) Written() bool {
	return writer.size >= 0
}

func (writer *taskSubmissionResponseWriter) Flush() {}

func (writer *taskSubmissionResponseWriter) Pusher() http.Pusher {
	return nil
}

func (writer *taskSubmissionResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("task submission response cannot be hijacked while buffered")
}

func (writer *taskSubmissionResponseWriter) CommitTo(target gin.ResponseWriter) error {
	if writer == nil || target == nil {
		return errors.New("task submission response writer is unavailable")
	}
	if writer.overflow {
		return errTaskSubmissionResponseTooLarge
	}
	for key, values := range writer.header {
		target.Header()[key] = append([]string(nil), values...)
	}
	target.WriteHeader(writer.Status())
	if writer.body.Len() == 0 {
		target.WriteHeaderNow()
		return nil
	}
	written, err := target.Write(writer.body.Bytes())
	if err == nil && written != writer.body.Len() {
		return io.ErrShortWrite
	}
	return err
}

func TaskSubmissionReplayHeaders(result *TaskSubmitResult) (string, error) {
	if result == nil || result.response == nil {
		return "", errors.New("task submission response is unavailable")
	}
	return safeAsyncBillingReplayHeaders(result.response.header)
}

// TaskSubmissionReplaySnapshot always returns a bounded fallback snapshot for
// an accepted upstream response. The error tells the caller to quarantine the
// handoff instead of committing with lossy response metadata.
func TaskSubmissionReplaySnapshot(
	result *TaskSubmitResult,
	publicTaskID string,
	modelName string,
) (model.AsyncBillingReplaySpec, error) {
	if result == nil || result.response == nil {
		return model.AsyncBillingReplaySpec{}, errors.New("task submission response is unavailable")
	}
	var snapshotErr error
	if result.response.overflow {
		snapshotErr = errTaskSubmissionResponseTooLarge
	}
	if !result.response.Written() {
		snapshotErr = errors.Join(snapshotErr, errors.New("task submission response was not written"))
	}
	if result.ResponseStatus() < http.StatusOK || result.ResponseStatus() >= http.StatusMultipleChoices {
		snapshotErr = errors.Join(snapshotErr, errors.New("task submission response status is not accepted"))
	}
	if identityErr := validateTaskSubmissionReplayBody(result.ResponseBody(), result.Platform, publicTaskID); identityErr != nil {
		snapshotErr = errors.Join(snapshotErr, identityErr)
	}
	headers, headerErr := TaskSubmissionReplayHeaders(result)
	if headerErr != nil {
		snapshotErr = errors.Join(snapshotErr, headerErr)
		headers = ""
	}
	contentType := strings.TrimSpace(result.ResponseContentType())
	if len(contentType) > 128 || !utf8.ValidString(contentType) || strings.ContainsAny(contentType, "\r\n\x00") {
		snapshotErr = errors.Join(snapshotErr, errors.New("task submission content type is invalid"))
		contentType = "application/json"
	}
	snapshot := model.AsyncBillingReplaySpec{
		StatusCode: result.ResponseStatus(), ContentType: contentType,
		HeadersJSON: headers, Body: result.ResponseBody(),
	}
	if snapshotErr == nil {
		return snapshot, nil
	}
	var fallback any
	if result.Platform == constant.TaskPlatformSuno {
		fallback = dto.TaskResponse[string]{Code: "success", Data: publicTaskID}
	} else {
		video := dto.NewOpenAIVideo()
		video.ID = publicTaskID
		video.TaskID = publicTaskID
		video.Model = modelName
		fallback = video
	}
	fallbackBody, err := common.Marshal(fallback)
	if err != nil {
		return model.AsyncBillingReplaySpec{}, errors.Join(snapshotErr, err)
	}
	return model.AsyncBillingReplaySpec{
		StatusCode: http.StatusOK, ContentType: "application/json", Body: fallbackBody,
	}, snapshotErr
}

func validateTaskSubmissionReplayBody(body []byte, platform constant.TaskPlatform, publicTaskID string) error {
	publicTaskID = strings.TrimSpace(publicTaskID)
	if publicTaskID == "" || len(body) == 0 {
		return errors.New("task submission response is missing the public task identity")
	}
	if platform == constant.TaskPlatformSuno {
		var response dto.TaskResponse[string]
		if err := common.Unmarshal(body, &response); err != nil {
			return fmt.Errorf("parse Suno submission replay response: %w", err)
		}
		if !response.IsSuccess() || strings.TrimSpace(response.Data) != publicTaskID {
			return errors.New("Suno submission response does not match the public task identity")
		}
		return nil
	}
	var response dto.OpenAIVideo
	if err := common.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("parse video submission replay response: %w", err)
	}
	if strings.TrimSpace(response.ID) != publicTaskID || strings.TrimSpace(response.TaskID) != publicTaskID {
		return errors.New("video submission response does not match the public task identity")
	}
	return nil
}

func (result *TaskSubmitResult) ResponseStatus() int {
	if result == nil || result.response == nil {
		return 0
	}
	return result.response.Status()
}

func (result *TaskSubmitResult) ResponseContentType() string {
	if result == nil || result.response == nil {
		return ""
	}
	return result.response.header.Get("Content-Type")
}

func (result *TaskSubmitResult) ResponseBody() []byte {
	if result == nil || result.response == nil {
		return nil
	}
	return append([]byte(nil), result.response.body.Bytes()...)
}
