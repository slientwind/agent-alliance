package a2a

// Alliance compatibility patch: the v1 specification requires ProtoJSON enum names.
// Keep SDK internal values stable for converters and accept early alpha values on read.
import (
	"encoding/json"
	"strings"
)

func (r MessageRole) MarshalJSON() ([]byte, error) {
	if r == "" {
		return json.Marshal("ROLE_UNSPECIFIED")
	}
	return json.Marshal("ROLE_" + strings.ToUpper(string(r)))
}
func (r *MessageRole) UnmarshalJSON(raw []byte) error {
	var value string
	if e := json.Unmarshal(raw, &value); e != nil {
		return e
	}
	value = strings.TrimPrefix(value, "ROLE_")
	if value == "UNSPECIFIED" {
		value = ""
	}
	*r = MessageRole(strings.ToLower(value))
	return nil
}
func (s TaskState) MarshalJSON() ([]byte, error) {
	if s == "" {
		return json.Marshal("TASK_STATE_UNSPECIFIED")
	}
	return json.Marshal("TASK_STATE_" + string(s))
}
func (s *TaskState) UnmarshalJSON(raw []byte) error {
	var value string
	if e := json.Unmarshal(raw, &value); e != nil {
		return e
	}
	value = strings.TrimPrefix(value, "TASK_STATE_")
	if value == "UNSPECIFIED" {
		value = ""
	}
	*s = TaskState(value)
	return nil
}
