package authz

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/igrybkov/ha-guest-access/internal/catalog"
	"github.com/igrybkov/ha-guest-access/internal/model"
)

// targetKeys are HA service target keys. A guest can never send one, even
// for an action that would otherwise accept the key: the server sets the
// target itself, to exactly one entity id.
var targetKeys = []string{"entity_id", "area_id", "device_id", "label_id", "floor_id"}

// checkData checks req.Data against the action's fields and returns a
// fresh map for the call.
//
// Rules:
//   - Scripts take no guest data at all.
//   - A target key or a key the action does not list is refused.
//   - Numbers must be float64 (what encoding/json gives) or json.Number.
//     NaN and Inf are refused. An int field needs an integral value.
//   - Values must sit in [Min, Max]. MinAttr and MaxAttr only narrow
//     those bounds, using st when it is the same entity. If the bounds
//     cross, every value is refused.
//   - Bools must be bool. Strings must be one of Enum.
//   - Anything else (arrays, objects, null) is refused.
func checkData(act catalog.Action, req Request, st *model.State) (map[string]any, error) {
	out := make(map[string]any, len(req.Data))
	if model.Domain(act.ID) == "script" && len(req.Data) > 0 {
		return nil, fmt.Errorf("%w: scripts take no data", ErrBadData)
	}
	keys := make([]string, 0, len(req.Data))
	for k := range req.Data {
		keys = append(keys, k)
	}
	slices.Sort(keys) // the first bad key is the same on every call
	for _, k := range keys {
		if slices.Contains(targetKeys, k) {
			return nil, fmt.Errorf("%w: %s: the target is set by the server", ErrBadData, quote(k))
		}
		f, ok := act.Data[k]
		if !ok {
			return nil, fmt.Errorf("%w: %s: unknown key", ErrBadData, quote(k))
		}
		v, err := checkValue(f, req.Data[k], req.EntityID, st)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrBadData, quote(k), err)
		}
		out[k] = v
	}
	return out, nil
}

func checkValue(f catalog.Field, v any, entityID string, st *model.State) (any, error) {
	switch f.Kind {
	case catalog.KindBool:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("must be true or false")
		}
		return b, nil
	case catalog.KindString:
		s, ok := v.(string)
		if !ok || !slices.Contains(f.Enum, s) {
			return nil, fmt.Errorf("must be one of %v", f.Enum)
		}
		return s, nil
	case catalog.KindInt, catalog.KindFloat:
		x, ok := guestNumber(v)
		if !ok {
			return nil, fmt.Errorf("must be a number")
		}
		lo, hi := bounds(f, entityID, st)
		if x < lo || x > hi {
			return nil, fmt.Errorf("must be between %v and %v", lo, hi)
		}
		if f.Kind == catalog.KindFloat {
			return x, nil
		}
		if x != math.Trunc(x) {
			return nil, fmt.Errorf("must be a whole number")
		}
		return int(x), nil
	}
	return nil, fmt.Errorf("has an unknown kind")
}

// guestNumber accepts only the number types a JSON decoder makes. It
// refuses strings that look like numbers, NaN and Inf.
func guestNumber(v any) (float64, bool) {
	var x float64
	switch v := v.(type) {
	case float64:
		x = v
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		x = f
	default:
		return 0, false
	}
	return x, !math.IsNaN(x) && !math.IsInf(x, 0)
}

// bounds returns the field's range, narrowed by the entity's attributes.
// Attributes come from HA, so any numeric Go type is taken.
func bounds(f catalog.Field, entityID string, st *model.State) (lo, hi float64) {
	lo, hi = f.Min, f.Max
	if st == nil || st.EntityID != entityID {
		return lo, hi
	}
	if x, ok := attrNumber(st.Attributes[f.MinAttr]); ok && f.MinAttr != "" {
		lo = max(lo, x)
	}
	if x, ok := attrNumber(st.Attributes[f.MaxAttr]); ok && f.MaxAttr != "" {
		hi = min(hi, x)
	}
	return lo, hi
}

func attrNumber(v any) (float64, bool) {
	switch v := v.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return guestNumber(v)
}

// quote quotes a guest-sent key for an error, cut to a sane length.
func quote(s string) string {
	const limit = 64
	if len(s) > limit {
		s = strings.ToValidUTF8(s[:limit], "") + "..."
	}
	return fmt.Sprintf("%q", s)
}
