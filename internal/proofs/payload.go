package proofs

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	documentVerificationType = "verification/document"
	passedStatus             = "passed"
	failedStatus             = "failed"
	ownerNameField           = "owner_name"
	propertyAddressField     = "property_address"
)

type documentFile struct {
	Filename string `json:"filename"`
	URL      string `json:"url"`
}

type extraction struct {
	FieldName string `json:"field-name"`
	Results   []struct {
		Value any `json:"value"`
	} `json:"results"`
}

type documentResult struct {
	Status      string         `json:"status"`
	CreatedAt   string         `json:"created-at"`
	Files       []documentFile `json:"files"`
	Extractions []extraction   `json:"extraction-responses"`
	Checks      []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"checks"`
}

func chosenDocument(body []byte) (documentResult, bool, error) {
	var envelope struct {
		Data struct {
			Attributes struct {
				Payload struct {
					Included []struct {
						Type       string          `json:"type"`
						Attributes json.RawMessage `json:"attributes"`
					} `json:"included"`
				} `json:"payload"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return documentResult{}, false, fmt.Errorf("decode persona payload: %w", err)
	}
	var documents []documentResult
	for _, object := range envelope.Data.Attributes.Payload.Included {
		if object.Type != documentVerificationType {
			continue
		}
		var document documentResult
		if err := json.Unmarshal(object.Attributes, &document); err != nil {
			return documentResult{}, false, fmt.Errorf("decode document verification: %w", err)
		}
		if len(document.Files) > 0 {
			documents = append(documents, document)
		}
	}
	if len(documents) == 0 {
		return documentResult{}, false, nil
	}
	return slices.MaxFunc(documents, compareDocuments), true, nil
}

func compareDocuments(left, right documentResult) int {
	if (left.Status == passedStatus) != (right.Status == passedStatus) {
		if left.Status == passedStatus {
			return 1
		}
		return -1
	}
	return createdAt(left).Compare(createdAt(right))
}

func createdAt(document documentResult) time.Time {
	at, err := time.Parse(time.RFC3339, document.CreatedAt)
	if err != nil {
		return time.Time{}
	}
	return at
}

func (document documentResult) clean() bool {
	if document.Status != passedStatus || len(document.Checks) == 0 {
		return false
	}
	for _, check := range document.Checks {
		if check.Status == failedStatus {
			return false
		}
	}
	return true
}

func (document documentResult) extracted(field string) (string, bool) {
	for _, response := range document.Extractions {
		if strings.ReplaceAll(strings.ToLower(response.FieldName), "-", "_") != field || len(response.Results) != 1 {
			continue
		}
		value, isText := response.Results[0].Value.(string)
		return value, isText && value != ""
	}
	return "", false
}
