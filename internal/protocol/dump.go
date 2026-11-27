package protocol

import "encoding/json"

// DumpItem identifies a complete current memory unit, independent of retrieval.
type DumpItem struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp,omitempty"`
	Layer     string `json:"layer,omitempty"`
}
type DumpResponse struct {
	Items []DumpItem `json:"items"`
}

func (i *DumpItem) UnmarshalJSON(b []byte) error {
	type plain DumpItem
	v := struct {
		*plain
		Text *string `json:"text"`
	}{plain: (*plain)(i)}
	if e := json.Unmarshal(b, &v); e != nil {
		return e
	}
	if v.Text == nil {
		return &json.UnmarshalTypeError{Value: "missing or null text", Type: stringType}
	}
	i.Text = *v.Text
	return nil
}
