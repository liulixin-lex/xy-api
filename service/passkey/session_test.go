package passkey

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingSaveSession struct {
	values  map[interface{}]interface{}
	saveErr error
}

func (session *failingSaveSession) ID() string { return "passkey-test-session" }

func (session *failingSaveSession) Get(key interface{}) interface{} {
	return session.values[key]
}

func (session *failingSaveSession) Set(key, value interface{}) {
	session.values[key] = value
}

func (session *failingSaveSession) Delete(key interface{}) {
	delete(session.values, key)
}

func (session *failingSaveSession) Clear() {
	clear(session.values)
}

func (*failingSaveSession) AddFlash(interface{}, ...string) {}

func (*failingSaveSession) Flashes(...string) []interface{} { return nil }

func (*failingSaveSession) Options(sessions.Options) {}

func (session *failingSaveSession) Save() error { return session.saveErr }

func TestPopSessionDataFailsClosedWhenChallengeConsumptionCannotBeSaved(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	saveErr := errors.New("session store unavailable")
	session := &failingSaveSession{
		values: map[interface{}]interface{}{
			"passkey_challenge": `{"challenge":"c2FmZS1jaGFsbGVuZ2U","userVerification":"required"}`,
		},
		saveErr: saveErr,
	}
	context.Set(sessions.DefaultKey, session)

	data, err := PopSessionData(context, "passkey_challenge")

	assert.Nil(t, data)
	assert.ErrorIs(t, err, saveErr)
	assert.Nil(t, session.Get("passkey_challenge"))
}

func TestPopSessionDataReturnsPersistedChallengeOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	payload := `{"challenge":"c2FmZS1jaGFsbGVuZ2U","userVerification":"required"}`
	session := &failingSaveSession{
		values: map[interface{}]interface{}{"passkey_challenge": payload},
	}
	context.Set(sessions.DefaultKey, session)

	data, err := PopSessionData(context, "passkey_challenge")

	require.NoError(t, err)
	require.NotNil(t, data)
	assert.Equal(t, "required", string(data.UserVerification))
	assert.Nil(t, session.Get("passkey_challenge"))
	_, err = PopSessionData(context, "passkey_challenge")
	assert.ErrorIs(t, err, errSessionNotFound)
}
