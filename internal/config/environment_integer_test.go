package config

import (
	"strings"
	"testing"
)

func TestParsePositiveIntegerEnvironmentVariable(t *testing.T) {
	const environmentVariable = "GROK_TEST_POSITIVE_INTEGER"

	testCases := []struct {
		name          string
		rawValue      string
		defaultValue  int
		expectedValue int
		expectedError string
	}{
		{name: "empty uses default", rawValue: "", defaultValue: 17, expectedValue: 17},
		{name: "whitespace uses default", rawValue: " \t ", defaultValue: 17, expectedValue: 17},
		{name: "trims valid value", rawValue: " 42 ", defaultValue: 17, expectedValue: 42},
		{name: "accepts explicit plus", rawValue: "+1", defaultValue: 17, expectedValue: 1},
		{name: "rejects zero", rawValue: "0", defaultValue: 17, expectedError: "must be a positive integer"},
		{name: "rejects negative", rawValue: "-1", defaultValue: 17, expectedError: "must be a positive integer"},
		{name: "rejects non-numeric", rawValue: "many", defaultValue: 17, expectedError: "must be a positive integer"},
		{name: "rejects overflow", rawValue: strings.Repeat("9", 100), defaultValue: 17, expectedError: "must be a positive integer"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(environmentVariable, testCase.rawValue)

			parsedValue, err := parsePositiveIntegerEnvironmentVariable(environmentVariable, testCase.defaultValue, "")
			if testCase.expectedError != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.expectedError) {
					t.Fatalf("error = %v, want message containing %q", err, testCase.expectedError)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse positive integer: %v", err)
			}
			if parsedValue != testCase.expectedValue {
				t.Fatalf("parsed value = %d, want %d", parsedValue, testCase.expectedValue)
			}
		})
	}
}

func TestParsePositiveIntegerEnvironmentVariableIncludesErrorSuffix(t *testing.T) {
	const environmentVariable = "GROK_TEST_TIMEOUT_SECONDS"
	t.Setenv(environmentVariable, " 0 ")

	_, err := parsePositiveIntegerEnvironmentVariable(environmentVariable, 120, " (seconds)")
	if err == nil {
		t.Fatal("expected invalid timeout error")
	}
	expectedMessage := `GROK_TEST_TIMEOUT_SECONDS must be a positive integer (seconds), got "0"`
	if err.Error() != expectedMessage {
		t.Fatalf("error = %q, want %q", err.Error(), expectedMessage)
	}
}
