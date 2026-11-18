package reconcile

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
)

// changedFields returns the JSON names of the fields in which want and have
// differ. Both must be structs of the same type.
func changedFields(want, have any) ([]string, error) {
	wantFields, err := jsonFields(want)
	if err != nil {
		return nil, err
	}
	haveFields, err := jsonFields(have)
	if err != nil {
		return nil, err
	}

	var changed []string
	for name, value := range wantFields {
		if !reflect.DeepEqual(value, haveFields[name]) {
			changed = append(changed, name)
		}
	}
	for name := range haveFields {
		if _, ok := wantFields[name]; !ok {
			changed = append(changed, name)
		}
	}
	slices.Sort(changed)
	return changed, nil
}

func jsonFields(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	// Fields that are omitted when empty and fields that are null are the
	// same to garm.
	maps.DeleteFunc(fields, func(_ string, value any) bool { return value == nil })
	return fields, nil
}
