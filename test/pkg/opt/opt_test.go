package opt_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/pkg/opt"
)

type sampleRequest struct {
	Name opt.Field[string] `json:"name"`
	Age  opt.Field[int]    `json:"age"`
}

func TestField_ZeroValueIsUnset(t *testing.T) {
	var f opt.Field[string]

	assert.True(t, f.IsUnset())
	assert.False(t, f.IsNull())
	assert.False(t, f.IsValue())
	assert.False(t, f.IsSet())
	assert.Empty(t, f.Value())
	assert.Nil(t, f.Ptr())

	val, ok := f.Get()
	assert.False(t, ok)
	assert.Empty(t, val)
	assert.Equal(t, "default", f.Or("default"))
	assert.Panics(t, func() { f.MustGet() })
}

func TestField_FromAndConstructors(t *testing.T) {
	t.Run("From value", func(t *testing.T) {
		f := opt.From("hello")
		assert.False(t, f.IsUnset())
		assert.False(t, f.IsNull())
		assert.True(t, f.IsValue())
		assert.True(t, f.IsSet())
		assert.Equal(t, "hello", f.Value())
		assert.Equal(t, "hello", f.MustGet())
		require.NotNil(t, f.Ptr())
		assert.Equal(t, "hello", *f.Ptr())
		assert.Equal(t, "hello", f.Or("default"))

		val, ok := f.Get()
		assert.True(t, ok)
		assert.Equal(t, "hello", val)
	})

	t.Run("Null", func(t *testing.T) {
		f := opt.Null[string]()
		assert.False(t, f.IsUnset())
		assert.True(t, f.IsNull())
		assert.False(t, f.IsValue())
		assert.True(t, f.IsSet())
		assert.Empty(t, f.Value())
		assert.Nil(t, f.Ptr())
		assert.Equal(t, "default", f.Or("default"))
		assert.Panics(t, func() { f.MustGet() })
	})

	t.Run("FromPtr with nil", func(t *testing.T) {
		var p *int
		f := opt.FromPtr(p)
		assert.True(t, f.IsNull())
		assert.False(t, f.IsValue())
	})

	t.Run("FromPtr with valid pointer", func(t *testing.T) {
		num := 42
		f := opt.FromPtr(&num)
		assert.True(t, f.IsValue())
		assert.Equal(t, 42, f.Value())
	})
}

func TestField_JSONUnmarshal(t *testing.T) {
	t.Run("Omitted fields remain unset", func(t *testing.T) {
		var req sampleRequest
		err := json.Unmarshal([]byte(`{}`), &req)
		require.NoError(t, err)

		assert.True(t, req.Name.IsUnset())
		assert.True(t, req.Age.IsUnset())
	})

	t.Run("Explicit null sets state to Null", func(t *testing.T) {
		var req sampleRequest
		err := json.Unmarshal([]byte(`{"name": null, "age": 25}`), &req)
		require.NoError(t, err)

		assert.False(t, req.Name.IsUnset())
		assert.True(t, req.Name.IsNull())
		assert.False(t, req.Name.IsValue())

		assert.False(t, req.Age.IsUnset())
		assert.False(t, req.Age.IsNull())
		assert.True(t, req.Age.IsValue())
		assert.Equal(t, 25, req.Age.Value())
	})

	t.Run("Valid value unmarshals properly", func(t *testing.T) {
		var req sampleRequest
		err := json.Unmarshal([]byte(`{"name": "Alice"}`), &req)
		require.NoError(t, err)

		assert.True(t, req.Name.IsValue())
		assert.Equal(t, "Alice", req.Name.Value())
		assert.True(t, req.Age.IsUnset())
	})

	t.Run("Invalid JSON type returns error", func(t *testing.T) {
		var req sampleRequest
		err := json.Unmarshal([]byte(`{"age": "not-an-int"}`), &req)
		require.Error(t, err)
	})
}

func TestField_JSONMarshal(t *testing.T) {
	req := sampleRequest{
		Name: opt.From("Bob"),
		Age:  opt.Null[int](),
	}

	data, err := json.Marshal(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"Bob","age":null}`, string(data))
}
