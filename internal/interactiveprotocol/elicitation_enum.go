package interactiveprotocol

import (
	"encoding/json"
	"fmt"
)

type elicitationEnumChoice struct {
	label, title string
	value        any
}

func elicitationEnumChoices(field map[string]any) []elicitationEnumChoice {
	var choices []elicitationEnumChoice
	if values, ok := field["enum"].([]any); ok {
		names, _ := field["enumNames"].([]any)
		for index, value := range values {
			label := fmt.Sprint(value)
			title := label
			if index < len(names) {
				if name, ok := names[index].(string); ok && name != "" {
					title = name
				}
			}
			choices = append(choices, elicitationEnumChoice{label, title, value})
		}
	} else if options, ok := field["oneOf"].([]any); ok {
		for _, option := range options {
			definition, ok := option.(map[string]any)
			if !ok {
				return nil
			}
			value, ok := definition["const"]
			if !ok {
				return nil
			}
			label := fmt.Sprint(value)
			title, _ := definition["title"].(string)
			if title == "" {
				title = label
			}
			choices = append(choices, elicitationEnumChoice{label, title, value})
		}
	}
	return choices
}

func elicitationFieldValue(field map[string]any, input string) (any, error) {
	for _, choice := range elicitationEnumChoices(field) {
		if choice.label == input {
			return choice.value, nil
		}
	}
	if field["type"] == "string" {
		return input, nil
	}
	var value any
	if err := json.Unmarshal([]byte(input), &value); err != nil {
		return nil, err
	}
	return value, nil
}
