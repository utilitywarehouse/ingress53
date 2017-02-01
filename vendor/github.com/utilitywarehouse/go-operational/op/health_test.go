package op

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestHealthCheck(t *testing.T) {
	assert := assert.New(t)

	hc := NewStatus("my app", "app description").
		AddChecker("check the foo bar", func(cr *CheckResponse) {
			cr.Healthy("check command completed ok")
		}).
		AddChecker("check the bar baz", func(cr *CheckResponse) {
			cr.Degraded("thing failed", "fix the thing")
		})

	result := hc.Check()

	expected := HealthResult{
		Name:        "my app",
		Description: "app description",
		Health:      "degraded",
		CheckResults: []healthResultEntry{
			{
				Name:   "check the foo bar",
				Health: "healthy",
				Output: "check command completed ok",
				Action: "",
				Impact: "",
			},
			{
				Name:   "check the bar baz",
				Health: "degraded",
				Output: "thing failed",
				Action: "fix the thing",
				Impact: "",
			},
		},
	}

	assert.Equal(expected, result)
}

func TestReadyNever(t *testing.T) {
	assert := assert.New(t)

	hc := NewStatus("my app", "app description").ReadyNever()
	assert.False(hc.ready())
}

func TestReadyAlways(t *testing.T) {
	assert := assert.New(t)

	hc := NewStatus("my app", "app description").ReadyAlways()
	assert.True(hc.ready())
}

func TestReadyFunc(t *testing.T) {
	assert := assert.New(t)
	var called bool
	hc := NewStatus("my app", "app description").Ready(func() bool { called = true; return true })
	assert.True(hc.ready())
	assert.True(called)
}

func TestReadyUseHealthCheck(t *testing.T) {
	assert := assert.New(t)

	var called bool
	_ = NewStatus("my app", "app description").
		AddChecker("check the bar baz", func(cr *CheckResponse) {
			called = true
		}).ReadyUseHealthCheck().ready()
	assert.True(called)

	healthyReady := NewStatus("my app", "app description").
		AddChecker("check the bar baz", func(cr *CheckResponse) {
			cr.Healthy("thing is healthy")
		}).ReadyUseHealthCheck().ready()
	assert.True(healthyReady)

	degradedReady := NewStatus("my app", "app description").
		AddChecker("check the bar baz", func(cr *CheckResponse) {
			cr.Degraded("thing is degraded", "fix the thing")
		}).ReadyUseHealthCheck().ready()
	assert.True(degradedReady)

	unhealthyReady := NewStatus("my app", "app description").
		AddChecker("check the bar baz", func(cr *CheckResponse) {
			cr.Unhealthy("thing has failed", "fix the thing", "users unable to use the thing application")
		}).ReadyUseHealthCheck().ready()
	assert.False(unhealthyReady)
}
