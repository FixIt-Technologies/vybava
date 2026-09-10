package claudeguards

import "encoding/json"

func jsonUnmarshalLoose(raw json.RawMessage, v any) error { return json.Unmarshal(raw, v) }
