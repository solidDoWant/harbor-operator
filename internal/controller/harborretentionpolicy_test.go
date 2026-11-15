package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRetentionScheduleValidation(t *testing.T) {
	tests := []struct {
		name     string
		schedule string
		valid    bool
	}{
		{name: "empty means manual runs only", schedule: "", valid: true},
		{name: "daily at midnight with seconds", schedule: "0 0 0 * * *", valid: true},
		{name: "every five minutes", schedule: "0 */5 * * * *", valid: true},
		{name: "five fields lack seconds", schedule: "0 0 * * *"},
		{name: "seven fields", schedule: "0 0 0 * * * *"},
		{name: "hour out of range", schedule: "0 0 25 * * *"},
		{name: "not a cron expression", schedule: "daily please"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := validateSchedule(test.schedule)
			if test.valid {
				assert.Empty(t, message)
			} else {
				assert.Contains(t, message, test.schedule)
			}
		})
	}
}
