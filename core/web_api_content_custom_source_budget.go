//go:build webpanel && !android

package core

import "time"

func webCustomSourceValueBudget(value any, remaining *int) error {
	charge := func(size int) error {
		if size > *remaining {
			return webAPIError(413, "content_too_large", "expanded custom import exceeds its aggregate byte limit")
		}
		*remaining -= size
		return nil
	}
	textSize := func(text string) int {
		size := 2
		for _, r := range text {
			switch {
			case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t' || r == '\b' || r == '\f':
				size += 2
			case r < 0x20 || r == '<' || r == '>' || r == '&' || r == '\u2028' || r == '\u2029':
				size += 6
			case r < 0x80:
				size++
			case r < 0x800:
				size += 2
			case r < 0x10000:
				size += 3
			default:
				size += 4
			}
			if size > *remaining {
				return size
			}
		}
		return size
	}
	switch item := value.(type) {
	case map[string]any:
		if err := charge(2 + len(item)*2); err != nil {
			return err
		}
		for key, value := range item {
			if err := charge(textSize(key)); err != nil {
				return err
			}
			if err := webCustomSourceValueBudget(value, remaining); err != nil {
				return err
			}
		}
	case []any:
		if err := charge(2 + len(item)); err != nil {
			return err
		}
		for _, value := range item {
			if err := webCustomSourceValueBudget(value, remaining); err != nil {
				return err
			}
		}
	case string:
		return charge(textSize(item))
	case nil, bool:
		return charge(5)
	case int, int64, uint64, float64, time.Time:
		return charge(32)
	default:
		return webCustomSourceError()
	}
	return nil
}
