package migration

import (
	"errors"
	"strings"
)

// decimal is a bounded, non-negative decimal value. It deliberately avoids
// floating point and math/big so offer comparisons remain exact without a
// pointer-heavy numeric object graph.
type decimal struct {
	digits string
	scale  int
}

func parseDecimal(value string) (decimal, error) {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 64 {
		return decimal{}, errors.New("invalid decimal")
	}
	dot := strings.IndexByte(value, '.')
	if dot >= 0 && strings.IndexByte(value[dot+1:], '.') >= 0 {
		return decimal{}, errors.New("invalid decimal")
	}
	integer, fraction := value, ""
	if dot >= 0 {
		integer, fraction = value[:dot], value[dot+1:]
		if integer == "" {
			integer = "0"
		}
		if fraction == "" {
			return decimal{}, errors.New("invalid decimal")
		}
	}
	if integer == "" {
		return decimal{}, errors.New("invalid decimal")
	}
	for _, character := range integer + fraction {
		if character < '0' || character > '9' {
			return decimal{}, errors.New("invalid decimal")
		}
	}
	digits := integer + fraction
	first := 0
	for first < len(digits)-1 && digits[first] == '0' {
		first++
	}
	digits = digits[first:]
	scale := len(fraction)
	for scale > 0 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		scale--
	}
	if digits == "" {
		return decimal{digits: "0"}, nil
	}
	return decimal{digits: digits, scale: scale}, nil
}

func decimalString(value decimal) string {
	if value.digits == "0" {
		return "0"
	}
	if value.scale == 0 {
		return value.digits
	}
	if value.scale >= len(value.digits) {
		return "0." + strings.Repeat("0", value.scale-len(value.digits)) + value.digits
	}
	position := len(value.digits) - value.scale
	return value.digits[:position] + "." + value.digits[position:]
}

func addDecimal(left, right decimal) decimal {
	scale := left.scale
	if right.scale > scale {
		scale = right.scale
	}
	leftDigits := left.digits + strings.Repeat("0", scale-left.scale)
	rightDigits := right.digits + strings.Repeat("0", scale-right.scale)
	sum := make([]byte, len(leftDigits)+1)
	i, j, position, carry := len(leftDigits)-1, len(rightDigits)-1, len(sum)-1, byte(0)
	for i >= 0 || j >= 0 || carry != 0 {
		value := carry
		if i >= 0 {
			value += leftDigits[i] - '0'
			i--
		}
		if j >= 0 {
			value += rightDigits[j] - '0'
			j--
		}
		sum[position] = '0' + value%10
		carry = value / 10
		position--
	}
	result := decimal{digits: string(sum[position+1:]), scale: scale}
	return normalizeDecimal(result)
}

func multiplyDecimal(value decimal, factor byte) decimal {
	if factor == 0 || value.digits == "0" {
		return decimal{digits: "0"}
	}
	result := make([]byte, len(value.digits)+1)
	position, carry := len(result)-1, byte(0)
	for index := len(value.digits) - 1; index >= 0; index-- {
		product := (value.digits[index]-'0')*factor + carry
		result[position] = '0' + product%10
		carry = product / 10
		position--
	}
	if carry > 0 {
		result[position] = '0' + carry
	} else {
		position++
	}
	return normalizeDecimal(decimal{digits: string(result[position:]), scale: value.scale})
}

func normalizeDecimal(value decimal) decimal {
	if value.digits == "" {
		return decimal{digits: "0"}
	}
	first := 0
	for first < len(value.digits)-1 && value.digits[first] == '0' {
		first++
	}
	value.digits = value.digits[first:]
	for value.scale > 0 && len(value.digits) > 1 && value.digits[len(value.digits)-1] == '0' {
		value.digits = value.digits[:len(value.digits)-1]
		value.scale--
	}
	if strings.Trim(value.digits, "0") == "" {
		return decimal{digits: "0"}
	}
	return value
}

func compareDecimal(left, right decimal) int {
	left, right = normalizeDecimal(left), normalizeDecimal(right)
	scale := left.scale
	if right.scale > scale {
		scale = right.scale
	}
	leftDigits := strings.TrimLeft(left.digits+strings.Repeat("0", scale-left.scale), "0")
	rightDigits := strings.TrimLeft(right.digits+strings.Repeat("0", scale-right.scale), "0")
	if leftDigits == "" {
		leftDigits = "0"
	}
	if rightDigits == "" {
		rightDigits = "0"
	}
	if len(leftDigits) < len(rightDigits) {
		return -1
	}
	if len(leftDigits) > len(rightDigits) {
		return 1
	}
	if leftDigits < rightDigits {
		return -1
	}
	if leftDigits > rightDigits {
		return 1
	}
	return 0
}

func maximumApprovedDecimal(value decimal) decimal {
	// baseline * 110% = baseline * 11 / 10. Increasing scale by one is the
	// exact division by ten; normalizeDecimal removes insignificant zeros.
	result := multiplyDecimal(value, 11)
	result.scale++
	return normalizeDecimal(result)
}
