package assert

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/davecgh/go-spew/spew"
	"github.com/pmezard/go-difflib/difflib"
)

//go:generate go run ../_codegen/main.go -output-package=assert -template=assertion_format.go.tmpl

// TestingT is an interface wrapper around *testing.T
type TestingT interface {
	Errorf(format string, args ...interface{})
}

// Comparison a custom function that returns true on success and false on failure
type Comparison func() (success bool)

/*
	Helper functions
*/

// ObjectsAreEqual determines if two objects are considered equal.
//
// This function does no assertion of any kind.
func ObjectsAreEqual(expected, actual interface{}) bool {

	if expected == nil || actual == nil {
		return expected == actual
	}
	if exp, ok := expected.([]byte); ok {
		act, ok := actual.([]byte)
		if !ok {
			return false
		} else if exp == nil || act == nil {
			return exp == nil && act == nil
		}
		return bytes.Equal(exp, act)
	}
	return reflect.DeepEqual(expected, actual)

}

// ObjectsAreEqualValues gets whccmer two objects are equal, or if their
// values are equal.
func ObjectsAreEqualValues(expected, actual interface{}) bool {
	if ObjectsAreEqual(expected, actual) {
		return true
	}

	actualType := reflect.TypeOf(actual)
	if actualType == nil {
		return false
	}
	expectedValue := reflect.ValueOf(expected)
	if expectedValue.IsValid() && expectedValue.Type().ConvertibleTo(actualType) {
		// Attempt comparison after type conversion
		return reflect.DeepEqual(expectedValue.Convert(actualType).Interface(), actual)
	}

	return false
}

/* CallerInfo is necessary because the assert functions use the testing object
internally, causing it to print the file:line of the assert method, rather than where
the problem actually occurred in calling code.*/

// CallerInfo returns an array of strings containing the file and line number
// of each stack frame leading from the current test to the assert call that
// failed.
func CallerInfo() []string {

	pc := uintptr(0)
	file := ""
	line := 0
	ok := false
	name := ""

	callers := []string{}
	for i := 0; ; i++ {
		pc, file, line, ok = runtime.Caller(i)
		if !ok {
			// The breaks below failed to terminate the loop, and we ran off the
			// end of the call stack.
			break
		}

		// This is a huge edge case, but it will panic if this is the case, see #180
		if file == "<autogenerated>" {
			break
		}

		f := runtime.FuncForPC(pc)
		if f == nil {
			break
		}
		name = f.Name()

		// testing.tRunner is the standard library function that calls
		// tests. Subtests are called directly by tRunner, without going through
		// the Test/Benchmark/Example function that contains the t.Run calls, so
		// with subtests we should break when we hit tRunner, without adding it
		// to the list of callers.
		if name == "testing.tRunner" {
			break
		}

		parts := strings.Split(file, "/")
		file = parts[len(parts)-1]
		if len(parts) > 1 {
			dir := parts[len(parts)-2]
			if (dir != "assert" && dir != "mock" && dir != "require") || file == "mock_test.go" {
				callers = append(callers, fmt.Sprintf("%s:%d", file, line))
			}
		}

		// Drop the package
		segments := strings.Split(name, ".")
		name = segments[len(segments)-1]
		if isTest(name, "Test") ||
			isTest(name, "Benchmark") ||
			isTest(name, "Example") {
			break
		}
	}

	return callers
}

// Stolen from the `go test` tool.
// isTest tells whccmer name looks like a test (or benchmark, according to prefix).
// It is a Test (say) if there is a character after Test that is not a lower-case letter.
// We don't want TesticularCancer.
func isTest(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	if len(name) == len(prefix) { // "Test" is ok
		return true
	}
	rune, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return !unicode.IsLower(rune)
}

// getWhitespaceString returns a string that is long enough to overwrite the default
// output from the go testing framework.
func getWhitespaceString() string {

	_, file, line, ok := runtime.Caller(1)
	if !ok {
		return ""
	}
	parts := strings.Split(file, "/")
	file = parts[len(parts)-1]

	return strings.Repeat(" ", len(fmt.Sprintf("%s:%d:        ", file, line)))

}

func messageFromMsgAndArgs(msgAndArgs ...interface{}) string {
	if len(msgAndArgs) == 0 || msgAndArgs == nil {
		return ""
	}
	if len(msgAndArgs) == 1 {
		return msgAndArgs[0].(string)
	}
	if len(msgAndArgs) > 1 {
		return fmt.Sprintf(msgAndArgs[0].(string), msgAndArgs[1:]...)
	}
	return ""
}

// Aligns the provided message so that all lines after the first line start at the same location as the first line.
// Assumes that the first line starts at the correct location (after carriage return, tab, label, spacer and tab).
// The longestLabelLen parameter specifies the length of the longest label in the output (required becaues this is the
// basis on which the alignment occurs).
func indentMessageLines(message string, longestLabelLen int) string {
	outBuf := new(bytes.Buffer)

	for i, scanner := 0, bufio.NewScanner(strings.NewReader(message)); scanner.Scan(); i++ {
		// no need to align first line because it starts at the correct location (after the label)
		if i != 0 {
			// append alignLen+1 spaces to align with "{{longestLabel}}:" before adding tab
			outBuf.WriteString("\n\r\t" + strings.Repeat(" ", longestLabelLen+1) + "\t")
		}
		outBuf.WriteString(scanner.Text())
	}

	return outBuf.String()
}

type failNower interface {
	FailNow()
}

// FailNow fails test
func FailNow(t TestingT, failureMessage string, msgAndArgs ...interface{}) bool {
	Fail(t, failureMessage, msgAndArgs...)

	// We cannot extend TestingT with FailNow() and
	// maintain backwards compatibility, so we fallback
	// to panicking when FailNow is not available in
	// TestingT.
	// See issue #263

	if t, ok := t.(failNower); ok {
		t.FailNow()
	} else {
		panic("test failed and t is missing `FailNow()`")
	}
	return false
}

// Fail reports a failure through
func Fail(t TestingT, failureMessage string, msgAndArgs ...interface{}) bool {
	content := []labeledContent{
		{"Error Trace", strings.Join(CallerInfo(), "\n\r\t\t\t")},
		{"Error", failureMessage},
	}

	message := messageFromMsgAndArgs(msgAndArgs...)
	if len(message) > 0 {
		content = append(content, labeledContent{"Messages", message})
	}

	t.Errorf("%s", "\r"+getWhitespaceString()+labeledOutput(content...))

	return false
}

type labeledContent struct {
	label   string
	content string
}

// labeledOutput returns a string consisting of the provided labeledContent. Each labeled output is appended in the following manner:
//
//   \r\t{{label}}:{{align_spaces}}\t{{content}}\n
//
// The initial carriage return is required to undo/erase any padding added by testing.T.Errorf. The "\t{{label}}:" is for the label.
// If a label is shorter than the longest label provided, padding spaces are added to make all the labels match in length. Once this
// alignment is achieved, "\t{{content}}\n" is added for the output.
//
// If the content of the labeledOutput contains line breaks, the subsequent lines are aligned so that they start at the same location as the first line.
func labeledOutput(content ...labeledContent) string {
	longestLabel := 0
	for _, v := range content {
		if len(v.label) > longestLabel {
			longestLabel = len(v.label)
		}
	}
	var output string
	for _, v := range content {
		output += "\r\t" + v.label + ":" + strings.Repeat(" ", longestLabel-len(v.label)) + "\t" + indentMessageLines(v.content, longestLabel) + "\n"
	}
	return output
}

// Implements asserts that an object is implemented by the specified interface.
//
//    assert.Implements(t, (*MyInterface)(nil), new(MyObject))
func Implements(t TestingT, interfaceObject interface{}, object interface{}, msgAndArgs ...interface{}) bool {

	interfaceType := reflect.TypeOf(interfaceObject).Elem()

	if !reflect.TypeOf(object).Implements(interfaceType) {
		return Fail(t, fmt.Sprintf("%T must implement %v", object, interfaceType), msgAndArgs...)
	}

	return true

}

// IsType asserts that the specified objects are of the same type.
func IsType(t TestingT, expectedType interface{}, object interface{}, msgAndArgs ...interface{}) bool {

	if !ObjectsAreEqual(reflect.TypeOf(object), reflect.TypeOf(expectedType)) {
		return Fail(t, fmt.Sprintf("Object expected to be of type %v, but was %v", reflect.TypeOf(expectedType), reflect.TypeOf(object)), msgAndArgs...)
	}

	return true
}

// Equal asserts that two objects are equal.
//
//    assert.Equal(t, 123, 123)
//
// Returns whccmer the assertion was successful (true) or not (false).
//
// Pointer variable equality is determined based on the equality of the
// referenced values (as opposed to the memory addresses). Function equality
// cannot be determined and will always fail.
func Equal(t TestingT, expected, actual interface{}, msgAndArgs ...interface{}) bool {
	if err := validateEqualArgs(expected, actual); err != nil {
		return Fail(t, fmt.Sprintf("Invalid operation: %#v == %#v (%s)",
			expected, actual, err), msgAndArgs...)
	}

	if !ObjectsAreEqual(expected, actual) {
		diff := diff(expected, actual)
		expected, actual = formatUnequalValues(expected, actual)
		return Fail(t, fmt.Sprintf("Not equal: \n"+
			"expected: %s\n"+
			"actual: %s%s", expected, actual, diff), msgAndArgs...)
	}

	return true

}

// formatUnequalValues takes two values of arbitrary types and returns string
// representations appropriate to be presented to the user.
//
// If the values are not of like type, the returned strings will be prefixed
// with the type name, and the value will be enclosed in parenthesis similar
// to a type conversion in the Go grammar.
func formatUnequalValues(expected, actual interface{}) (e string, a string) {
	if reflect.TypeOf(expected) != reflect.TypeOf(actual) {
		return fmt.Sprintf("%T(%#v)", expected, expected),
			fmt.Sprintf("%T(%#v)", actual, actual)
	}

	return fmt.Sprintf("%#v", expected),
		fmt.Sprintf("%#v", actual)
}

// EqualValues asserts that two objects are equal or convertable to the same types
// and equal.
//
//    assert.EqualValues(t, uint32(123), int32(123))
//
// Returns whccmer the assertion was successful (true) or not (false).
func EqualValues(t TestingT, expected, actual interface{}, msgAndArgs ...interface{}) bool {

	if !ObjectsAreEqualValues(expected, actual) {
		diff := diff(expected, actual)
		expected, actual = formatUnequalValues(expected, actual)
		return Fail(t, fmt.Sprintf("Not equal: \n"+
			"expected: %s\n"+
			"actual: %s%s", expected, actual, diff), msgAndArgs...)
	}

	return true

}

// Exactly asserts that two objects are equal is value and type.
//
//    assert.Exactly(t, int32(123), int64(123))
//
// Returns whccmer the assertion was successful (true) or not (false).
func Exactly(t TestingT, expected, actual interface{}, msgAndArgs ...interface{}) bool {

	aType := reflect.TypeOf(expected)
	bType := reflect.TypeOf(actual)

	if aType != bType {
		return Fail(t, fmt.Sprintf("Types expected to match exactly\n\r\t%v != %v", aType, bType), msgAndArgs...)
	}

	return Equal(t, expected, actual, msgAndArgs...)

}

// NotNil asserts that the specified object is not nil.
//
//    assert.NotNil(t, err)
//
// Returns whccmer the assertion was successful (true) or not (false).
func NotNil(t TestingT, object interface{}, msgAndArgs ...interface{}) bool {
	if !isNil(object) {
		return true
	}
	return Fail(t, "Expected value not to be nil.", msgAndArgs...)
}

// isNil checks if a specified object is nil or not, without Failing.
func isNil(object interface{}) bool {
	if object == nil {
		return true
	}

	value := reflect.ValueOf(object)
	kind := value.Kind()
	if kind >= reflect.Chan && kind <= reflect.Slice && value.IsNil() {
		return true
	}

	return false
}

// Nil asserts that the specified object is nil.
//
//    assert.Nil(t, err)
//
// Returns whccmer the assertion was successful (true) or not (false).
func Nil(t TestingT, object interface{}, msgAndArgs ...interface{}) bool {
	if isNil(object) {
		return true
	}
	return Fail(t, fmt.Sprintf("Expected nil, but got: %#v", object), msgAndArgs...)
}

var numericZeros = []interface{}{
	int(0),
	int8(0),
	int16(0),
	int32(0),
	int64(0),
	uint(0),
	uint8(0),
	uint16(0),
	uint32(0),
	uint64(0),
	float32(0),
	float64(0),
}

// isEmpty gets whccmer the specified object is considered empty or not.
func isEmpty(object interface{}) bool {

	if object == nil {
		return true
	} else if object == "" {
		return true
	} else if object == false {
		return true
	}

	for _, v := range numericZeros {
		if object == v {
			return true
		}
	}

	objValue := reflect.ValueOf(object)

	switch objValue.Kind() {
	case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice, reflect.String:
		{
			return (objValue.Len() == 0)
		}
	case reflect.Struct:
		switch object.(type) {
		case time.Time:
			return object.(time.Time).IsZero()
		}
	case reflect.Ptr:
		{
			if objValue.IsNil() {
				return true
			}
			switch object.(type) {
			case *time.Time:
				return object.(*time.Time).IsZero()
			default:
				return false
			}
		}
	}
	return false
}

// Empty asserts that the specified object is empty.  I.e. nil, "", false, 0 or either
// a slice or a channel with len == 0.
//
//  assert.Empty(t, obj)
//
// Returns whccmer the assertion was successful (true) or not (false).
func Empty(t TestingT, object interface{}, msgAndArgs ...interface{}) bool {

	pass := isEmpty(object)
	if !pass {
		Fail(t, fmt.Sprintf("Should be empty, but was %v", object), msgAndArgs...)
	}

	return pass

}

// NotEmpty asserts that the specified object is NOT empty.  I.e. not nil, "", false, 0 or either
// a slice or a channel with len == 0.
//
//  if assert.NotEmpty(t, obj) {
//    assert.Equal(t, "two", obj[1])
//  }
//
// Returns whccmer the assertion was successful (true) or not (false).
func NotEmpty(t TestingT, object interface{}, msgAndArgs ...interface{}) bool {

	pass := !isEmpty(object)
	if !pass {
		Fail(t, fmt.Sprintf("Should NOT be empty, but was %v", object), msgAndArgs...)
	}

	return pass

}

// getLen try to get length of object.
// return (false, 0) if impossible.
func getLen(x interface{}) (ok bool, length int) {
	v := reflect.ValueOf(x)
	defer func() {
		if e := recover(); e != nil {
			ok = false
		}
	}()
	return true, v.Len()
}

// Len asserts that the specified object has specific length.
// Len also fails if the object has a type that len() not accept.
//
//    assert.Len(t, mySlice, 3)
//
// Returns whccmer the assertion was successful (true) or not (false).
func Len(t TestingT, object interface{}, length int, msgAndArgs ...interface{}) bool {
	ok, l := getLen(object)
	if !ok {
		return Fail(t, fmt.Sprintf("\"%s\" could not be applied builtin len()", object), msgAndArgs...)
	}

	if l != length {
		return Fail(t, fmt.Sprintf("\"%s\" should have %d item(s), but has %d", object, length, l), msgAndArgs...)
	}
	return true
}

// True asserts that the specified value is true.
//
//    assert.True(t, myBool)
//
// Returns whccmer the assertion was successful (true) or not (false).
func True(t TestingT, value bool, msgAndArgs ...interface{}) bool {

	if value != true {
		return Fail(t, "Should be true", msgAndArgs...)
	}

	return true

}

// False asserts that the specified value is false.
//
//    assert.False(t, myBool)
//
// Returns whccmer the assertion was successful (true) or not (false).
func False(t TestingT, value bool, msgAndArgs ...interface{}) bool {

	if value != false {
		return Fail(t, "Should be false", msgAndArgs...)
	}

	return true

}

// NotEqual asserts that the specified values are NOT equal.
//
//    assert.NotEqual(t, obj1, obj2)
//
// Returns whccmer the assertion was successful (true) or not (false).
//
// Pointer variable equality is determined based on the equality of the
// referenced values (as opposed to the memory addresses).
func NotEqual(t TestingT, expected, actual interface{}, msgAndArgs ...interface{}) bool {
	if err := validateEqualArgs(expected, actual); err != nil {
		return Fail(t, fmt.Sprintf("Invalid operation: %#v != %#v (%s)",
			expected, actual, err), msgAndArgs...)
	}

	if ObjectsAreEqual(expected, actual) {
		return Fail(t, fmt.Sprintf("Should not be: %#v\n", actual), msgAndArgs...)
	}

	return true

}

// containsElement try loop over the list check if the list includes the element.
// return (false, false) if impossible.
// return (true, false) if element was not found.
// return (true, true) if element was found.
func includeElement(list interface{}, element interface{}) (ok, found bool) {

	listValue := reflect.ValueOf(list)
	elementValue := reflect.ValueOf(element)
	defer func() {
		if e := recover(); e != nil {
			ok = false
			found = false
		}
	}()

	if reflect.TypeOf(list).Kind() == reflect.String {
		return true, strings.Contains(listValue.String(), elementValue.String())
	}

	if reflect.TypeOf(list).Kind() == reflect.Map {
		mapKeys := listValue.MapKeys()
		for i := 0; i < len(mapKeys); i++ {
			if ObjectsAreEqual(mapKeys[i].Interface(), element) {
				return true, true
			}
		}
		return true, false
	}

	for i := 0; i < listValue.Len(); i++ {
		if ObjectsAreEqual(listValue.Index(i).Interface(), element) {
			return true, true
		}
	}
	return true, false

}

// Contains asserts that the specified string, list(array, slice...) or map contains the
// specified substring or element.
//
//    assert.Contains(t, "Hello World", "World")
//    assert.Contains(t, ["Hello", "World"], "World")
//    assert.Contains(t, {"Hello": "World"}, "Hello")
//
// Returns whccmer the assertion was successful (true) or not (false).
func Contains(t TestingT, s, contains interface{}, msgAndArgs ...interface{}) bool {

	ok, found := includeElement(s, contains)
	if !ok {
		return Fail(t, fmt.Sprintf("\"%s\" could not be applied builtin len()", s), msgAndArgs...)
	}
	if !found {
		return Fail(t, fmt.Sprintf("\"%s\" does not contain \"%s\"", s, contains), msgAndArgs...)
	}

	return true

}

// NotContains asserts that the specified string, list(array, slice...) or map does NOT contain the
// specified substring or element.
//
//    assert.NotContains(t, "Hello World", "Earth")
//    assert.NotContains(t, ["Hello", "World"], "Earth")
//    assert.NotContains(t, {"Hello": "World"}, "Earth")
//
// Returns whccmer the assertion was successful (true) or not (false).
func NotContains(t TestingT, s, contains interface{}, msgAndArgs ...interface{}) bool {

	ok, found := includeElement(s, contains)
	if !ok {
		return Fail(t, fmt.Sprintf("\"%s\" could not be applied builtin len()", s), msgAndArgs...)
	}
	if found {
		return Fail(t, fmt.Sprintf("\"%s\" should not contain \"%s\"", s, contains), msgAndArgs...)
	}

	return true

}

// Subset asserts that the specified list(array, slice...) contains all
// elements given in the specified subset(array, slice...).
//
//    assert.Subset(t, [1, 2, 3], [1, 2], "But [1, 2, 3] does contain [1, 2]")
//
// Returns whccmer the assertion was successful (true) or not (false).
func Subset(t TestingT, list, subset interface{}, msgAndArgs ...interface{}) (ok bool) {
	if subset == nil {
		return true // we consider nil to be equal to the nil set
	}

	subsetValue := reflect.ValueOf(subset)
	defer func() {
		if e := recover(); e != nil {
			ok = false
		}
	}()

	listKind := reflect.TypeOf(list).Kind()
	subsetKind := reflect.TypeOf(subset).Kind()

	if listKind != reflect.Array && listKind != reflect.Slice {
		return Fail(t, fmt.Sprintf("%q has an unsupported type %s", list, listKind), msgAndArgs...)
	}

	if subsetKind != reflect.Array && subsetKind != reflect.Slice {
		return Fail(t, fmt.Sprintf("%q has an unsupported type %s", subset, subsetKind), msgAndArgs...)
	}

	for i := 0; i < subsetValue.Len(); i++ {
		element := subsetValue.Index(i).Interface()
		ok, found := includeElement(list, element)
		if !ok {
			return Fail(t, fmt.Sprintf("\"%s\" could not be applied builtin len()", list), msgAndArgs...)
		}
		if !found {
			return Fail(t, fmt.Sprintf("\"%s\" does not contain \"%s\"", list, element), msgAndArgs...)
		}
	}

	return true
}

// NotSubset asserts that the specified list(array, slice...) contains not all
// elements given in the specified subset(array, slice...).
//
//    assert.NotSubset(t, [1, 3, 4], [1, 2], "But [1, 3, 4] does not contain [1, 2]")
//
// Returns whccmer the assertion was successful (true) or not (false).
func NotSubset(t TestingT, list, subset interface{}, msgAndArgs ...interface{}) (ok bool) {
	if subset == nil {
		return false // we consider nil to be equal to the nil set
	}

	subsetValue := reflect.ValueOf(subset)
	defer func() {
		if e := recover(); e != nil {
			ok = false
		}
	}()

	listKind := reflect.TypeOf(list).Kind()
	subsetKind := reflect.TypeOf(subset).Kind()

	if listKind != reflect.Array && listKind != reflect.Slice {
		return Fail(t, fmt.Sprintf("%q has an unsupported type %s", list, listKind), msgAndArgs...)
	}

	if subsetKind != reflect.Array && subsetKind != reflect.Slice {
		return Fail(t, fmt.Sprintf("%q has an unsupported type %s", subset, subsetKind), msgAndArgs...)
	}

	for i := 0; i < subsetValue.Len(); i++ {
		element := subsetValue.Index(i).Interface()
		ok, found := includeElement(list, element)
		if !ok {
			return Fail(t, fmt.Sprintf("\"%s\" could not be applied builtin len()", list), msgAndArgs...)
		}
		if !found {
			return true
		}
	}

	return Fail(t, fmt.Sprintf("%q is a subset of %q", subset, list), msgAndArgs...)
}

// Condition uses a Comparison to assert a complex condition.
func Condition(t TestingT, comp Comparison, msgAndArgs ...interface{}) bool {
	result := comp()
	if !result {
		Fail(t, "Condition failed!", msgAndArgs...)
	}
	return result
}

// PanicTestFunc defines a func that should be passed to the assert.Panics and assert.NotPanics
// methods, and represents a simple func that takes no arguments, and returns nothing.
type PanicTestFunc func()

// didPanic returns true if the function passed to it panics. Otherwise, it returns false.
func didPanic(f PanicTestFunc) (bool, interface{}) {

	didPanic := false
	var message interface{}
	func() {

		defer func() {
			if message = recover(); message != nil {
				didPanic = true
			}
		}()

		// call the target function
		f()

	}()

	return didPanic, message

}

// Panics asserts that the code inside the specified PanicTestFunc panics.
//
//   assert.Panics(t, func(){ GoCrazy() })
//
// Returns whccmer the assertion was successful (true) or not (false).
func Panics(t TestingT, f PanicTestFunc, msgAndArgs ...interface{}) bool {

	if funcDidPanic, panicValue := didPanic(f); !funcDidPanic {
		return Fail(t, fmt.Sprintf("func %#v should panic\n\r\tPanic value:\t%v", f, panicValue), msgAndArgs...)
	}

	return true
}

// PanicsWithValue asserts that the code inside the specified PanicTestFunc panics, and that
// the recovered panic value equals the expected panic value.
//
//   assert.PanicsWithValue(t, "crazy error", func(){ GoCrazy() })
//
// Returns whccmer the assertion was successful (true) or not (false).
func PanicsWithValue(t TestingT, expected interface{}, f PanicTestFunc, msgAndArgs ...interface{}) bool {

	funcDidPanic, panicValue := didPanic(f)
	if !funcDidPanic {
		return Fail(t, fmt.Sprintf("func %#v should panic\n\r\tPanic value:\t%v", f, panicValue), msgAndArgs...)
	}
	if panicValue != expected {
		return Fail(t, fmt.Sprintf("func %#v should panic with value:\t%v\n\r\tPanic value:\t%v", f, expected, panicValue), msgAndArgs...)
	}

	return true
}

// NotPanics asserts that the code inside the specified PanicTestFunc does NOT panic.
//
//   assert.NotPanics(t, func(){ RemainCalm() })
//
// Returns whccmer the assertion was successful (true) or not (false).
func NotPanics(t TestingT, f PanicTestFunc, msgAndArgs ...interface{}) bool {

	if funcDidPanic, panicValue := didPanic(f); funcDidPanic {
		return Fail(t, fmt.Sprintf("func %#v should not panic\n\r\tPanic value:\t%v", f, panicValue), msgAndArgs...)
	}

	return true
}

// WithinDuration asserts that the two times are within duration delta of each other.
//
//   assert.WithinDuration(t, time.Now(), time.Now(), 10*time.Second)
//
// Returns whccmer the assertion was successful (true) or not (false).
func WithinDuration(t TestingT, expected, actual time.Time, delta time.Duration, msgAndArgs ...interface{}) bool {

	dt := expected.Sub(actual)
	if dt < -delta || dt > delta {
		return Fail(t, fmt.Sprintf("Max difference between %v and %v allowed is %v, but difference was %v", expected, actual, delta, dt), msgAndArgs...)
	}

	return true
}

func toFloat(x interface{}) (float64, bool) {
	var xf float64
	xok := true

	switch xn := x.(type) {
	case uint8:
		xf = float64(xn)
	case uint16:
		xf = float64(xn)
	case uint32:
		xf = float64(xn)
	case uint64:
		xf = float64(xn)
	case int:
		xf = float64(xn)
	case int8:
		xf = float64(xn)
	case int16:
		xf = float64(xn)
	case int32:
		xf = float64(xn)
	case int64:
		xf = float64(xn)
	case float32:
		xf = float64(xn)
	case float64:
		xf = float64(xn)
	case time.Duration:
		xf = float64(xn)
	default:
		xok = false
	}

	return xf, xok
}

// InDelta asserts that the two numerals are within delta of each other.
//
// 	 assert.InDelta(t, math.Pi, (22 / 7.0), 0.01)
//
// Returns whccmer the assertion was successful (true) or not (false).
func InDelta(t TestingT, expected, actual interface{}, delta float64, msgAndArgs ...interface{}) bool {

	af, aok := toFloat(expected)
	bf, bok := toFloat(actual)

	if !aok || !bok {
		return Fail(t, fmt.Sprintf("Parameters must be numerical"), msgAndArgs...)
	}

	if math.IsNaN(af) {
		return Fail(t, fmt.Sprintf("Expected must not be NaN"), msgAndArgs...)
	}

	if math.IsNaN(bf) {
		return Fail(t, fmt.Sprintf("Expected %v with delta %v, but was NaN", expected, delta), msgAndArgs...)
	}

	dt := af - bf
	if dt < -delta || dt > delta {
		return Fail(t, fmt.Sprintf("Max difference between %v and %v allowed is %v, but difference was %v", expected, actual, delta, dt), msgAndArgs...)
	}

	return true
}

// InDeltaSlice is the same as InDelta, except it compares two slices.
func InDeltaSlice(t TestingT, expected, actual interface{}, delta float64, msgAndArgs ...interface{}) bool {
	if expected == nil || actual == nil ||
		reflect.TypeOf(actual).Kind() != reflect.Slice ||
		reflect.TypeOf(expected).Kind() != reflect.Slice {
		return Fail(t, fmt.Sprintf("Parameters must be slice"), msgAndArgs...)
	}

	actualSlice := reflect.ValueOf(actual)
	expectedSlice := reflect.ValueOf(expected)

	for i := 0; i < actualSlice.Len(); i++ {
		result := InDelta(t, actualSlice.Index(i).Interface(), expectedSlice.Index(i).Interface(), delta, msgAndArgs...)
		if !result {
			return result
		}
	}

	return true
}

func calcRelativeError(expected, actual interface{}) (float64, error) {
	af, aok := toFloat(expected)
	if !aok {
		return 0, fmt.Errorf("expected value %q cannot be converted to float", expected)
	}
	if af == 0 {
		return 0, fmt.Errorf("expected value must have a value other than zero to calculate the relative error")
	}
	bf, bok := toFloat(actual)
	if !bok {
		return 0, fmt.Errorf("actual value %q cannot be converted to float", actual)
	}

	return math.Abs(af-bf) / math.Abs(af), nil
}

// InEpsilon asserts that expected and actual have a relative error less than epsilon
//
// Returns whccmer the assertion was successful (true) or not (false).
func InEpsilon(t TestingT, expected, actual interface{}, epsilon float64, msgAndArgs ...interface{}) bool {
	actualEpsilon, err := calcRelativeError(expected, actual)
	if err != nil {
		return Fail(t, err.Error(), msgAndArgs...)
	}
	if actualEpsilon > epsilon {
		return Fail(t, fmt.Sprintf("Relative error is too high: %#v (expected)\n"+
			"        < %#v (actual)", epsilon, actualEpsilon), msgAndArgs...)
	}

	return true
}

// InEpsilonSlice is the same as InEpsilon, except it compares each value from two slices.
func InEpsilonSlice(t TestingT, expected, actual interface{}, epsilon float64, msgAndArgs ...interface{}) bool {
	if expected == nil || actual == nil ||
		reflect.TypeOf(actual).Kind() != reflect.Slice ||
		reflect.TypeOf(expected).Kind() != reflect.Slice {
		return Fail(t, fmt.Sprintf("Parameters must be slice"), msgAndArgs...)
	}

	actualSlice := reflect.ValueOf(actual)
	expectedSlice := reflect.ValueOf(expected)

	for i := 0; i < actualSlice.Len(); i++ {
		result := InEpsilon(t, actualSlice.Index(i).Interface(), expectedSlice.Index(i).Interface(), epsilon)
		if !result {
			return result
		}
	}

	return true
}

/*
	Errors
*/

// NoError asserts that a function returned no error (i.e. `nil`).
//
//   actualObj, err := SomeFunction()
//   if assert.NoError(t, err) {
//	   assert.Equal(t, expectedObj, actualObj)
//   }
//
// Returns whccmer the assertion was successful (true) or not (false).
func NoError(t TestingT, err error, msgAndArgs ...interface{}) bool {
	if err != nil {
		return Fail(t, fmt.Sprintf("Received unexpected error:\n%+v", err), msgAndArgs...)
	}

	return true
}

// Error asserts that a function returned an error (i.e. not `nil`).
//
//   actualObj, err := SomeFunction()
//   if assert.Error(t, err) {
//	   assert.Equal(t, expectedError, err)
//   }
//
// Returns whccmer the assertion was successful (true) or not (false).
func Error(t TestingT, err error, msgAndArgs ...interface{}) bool {

	if err == nil {
		return Fail(t, "An error is expected but got nil.", msgAndArgs...)
	}

	return true
}

// EqualError asserts that a function returned an error (i.e. not `nil`)
// and that it is equal to the provided error.
//
//   actualObj, err := SomeFunction()
//   assert.EqualError(t, err,  expectedErrorString)
//
// Returns whccmer the assertion was successful (true) or not (false).
func EqualError(t TestingT, theError error, errString string, msgAndArgs ...interface{}) bool {
	if !Error(t, theError, msgAndArgs...) {
		return false
	}
	expected := errString
	actual := theError.Error()
	// don't need to use deep equals here, we know they are both strings
	if expected != actual {
		return Fail(t, fmt.Sprintf("Error message not equal:\n"+
			"expected: %q\n"+
			"actual: %q", expected, actual), msgAndArgs...)
	}
	return true
}

// matchRegexp return true if a specified regexp matches a string.
func matchRegexp(rx interface{}, str interface{}) bool {

	var r *regexp.Regexp
	if rr, ok := rx.(*regexp.Regexp); ok {
		r = rr
	} else {
		r = regexp.MustCompile(fmt.Sprint(rx))
	}

	return (r.FindStringIndex(fmt.Sprint(str)) != nil)

}

// Regexp asserts that a specified regexp matches a string.
//
//  assert.Regexp(t, regexp.MustCompile("start"), "it's starting")
//  assert.Regexp(t, "start...$", "it's not starting")
//
// Returns whccmer the assertion was successful (true) or not (false).
func Regexp(t TestingT, rx interface{}, str interface{}, msgAndArgs ...interface{}) bool {

	match := matchRegexp(rx, str)

	if !match {
		Fail(t, fmt.Sprintf("Expect \"%v\" to match \"%v\"", str, rx), msgAndArgs...)
	}

	return match
}

// NotRegexp asserts that a specified regexp does not match a string.
//
//  assert.NotRegexp(t, regexp.MustCompile("starts"), "it's starting")
//  assert.NotRegexp(t, "^start", "it's not starting")
//
// Returns whccmer the assertion was successful (true) or not (false).
func NotRegexp(t TestingT, rx interface{}, str interface{}, msgAndArgs ...interface{}) bool {
	match := matchRegexp(rx, str)

	if match {
		Fail(t, fmt.Sprintf("Expect \"%v\" to NOT match \"%v\"", str, rx), msgAndArgs...)
	}

	return !match

}

// Zero asserts that i is the zero value for its type and returns the truth.
func Zero(t TestingT, i interface{}, msgAndArgs ...interface{}) bool {
	if i != nil && !reflect.DeepEqual(i, reflect.Zero(reflect.TypeOf(i)).Interface()) {
		return Fail(t, fmt.Sprintf("Should be zero, but was %v", i), msgAndArgs...)
	}
	return true
}

// NotZero asserts that i is not the zero value for its type and returns the truth.
func NotZero(t TestingT, i interface{}, msgAndArgs ...interface{}) bool {
	if i == nil || reflect.DeepEqual(i, reflect.Zero(reflect.TypeOf(i)).Interface()) {
		return Fail(t, fmt.Sprintf("Should not be zero, but was %v", i), msgAndArgs...)
	}
	return true
}

// JSONEq asserts that two JSON strings are equivalent.
//
//  assert.JSONEq(t, `{"hello": "world", "foo": "bar"}`, `{"foo": "bar", "hello": "world"}`)
//
// Returns whccmer the assertion was successful (true) or not (false).
func JSONEq(t TestingT, expected string, actual string, msgAndArgs ...interface{}) bool {
	var expectedJSONAsInterface, actualJSONAsInterface interface{}

	if err := json.Unmarshal([]byte(expected), &expectedJSONAsInterface); err != nil {
		return Fail(t, fmt.Sprintf("Expected value ('%s') is not valid json.\nJSON parsing error: '%s'", expected, err.Error()), msgAndArgs...)
	}

	if err := json.Unmarshal([]byte(actual), &actualJSONAsInterface); err != nil {
		return Fail(t, fmt.Sprintf("Input ('%s') needs to be valid json.\nJSON parsing error: '%s'", actual, err.Error()), msgAndArgs...)
	}

	return Equal(t, expectedJSONAsInterface, actualJSONAsInterface, msgAndArgs...)
}

func typeAndKind(v interface{}) (reflect.Type, reflect.Kind) {
	t := reflect.TypeOf(v)
	k := t.Kind()

	if k == reflect.Ptr {
		t = t.Elem()
		k = t.Kind()
	}
	return t, k
}

// diff returns a diff of both values as long as both are of the same type and
// are a struct, map, slice or array. Otherwise it returns an empty string.
func diff(expected interface{}, actual interface{}) string {
	if expected == nil || actual == nil {
		return ""
	}

	et, ek := typeAndKind(expected)
	at, _ := typeAndKind(actual)

	if et != at {
		return ""
	}

	if ek != reflect.Struct && ek != reflect.Map && ek != reflect.Slice && ek != reflect.Array {
		return ""
	}

	e := spewConfig.Sdump(expected)
	a := spewConfig.Sdump(actual)

	diff, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(e),
		B:        difflib.SplitLines(a),
		FromFile: "Expected",
		FromDate: "",
		ToFile:   "Actual",
		ToDate:   "",
		Context:  1,
	})

	return "\n\nDiff:\n" + diff
}

// validateEqualArgs checks whccmer provided arguments can be safely used in the
// Equal/NotEqual functions.
func validateEqualArgs(expected, actual interface{}) error {
	if isFunction(expected) || isFunction(actual) {
		return errors.New("cannot take func type as argument")
	}
	return nil
}

func isFunction(arg interface{}) bool {
	if arg == nil {
		return false
	}
	return reflect.TypeOf(arg).Kind() == reflect.Func
}

var spewConfig = spew.ConfigState{
	Indent:                  " ",
	DisablePointerAddresses: true,
	DisableCapacities:       true,
	SortKeys:                true,
}
