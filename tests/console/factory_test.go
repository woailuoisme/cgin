package console_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/console"
	"bgin/internal/console/core"
)

func TestIOStreams_BufferingAndColor(t *testing.T) {
	t.Parallel()

	streams, in, out, errOut := core.TestIOStreams()
	require.NotNil(t, streams)
	assert.False(t, streams.CanPrompt())
	assert.False(t, streams.ColorEnabled())

	// 测试标准输出缓冲
	_, err := fmt.Fprint(streams.Out, "hello stdout")
	require.NoError(t, err)
	assert.Equal(t, "hello stdout", out.String())

	// 测试标准错误缓冲
	_, err = fmt.Fprint(streams.ErrOut, "warning stderr")
	require.NoError(t, err)
	assert.Equal(t, "warning stderr", errOut.String())

	// 测试标准输入读取
	in.WriteString("hello\n")
	var readStr string
	_, err = fmt.Fscanln(streams.In, &readStr)
	require.NoError(t, err)
	assert.Equal(t, "hello", readStr)

	// 色彩开关
	streams.SetColorEnabled(true)
	assert.True(t, streams.ColorEnabled())
}

func TestMockPrompter_Responses(t *testing.T) {
	t.Parallel()

	mock := core.NewMockPrompter()
	mock.ConfirmResponses = []bool{true, false}
	mock.InputResponses = []string{"article", "custom_tag"}
	mock.SelectResponses = []string{"admin", "client"}

	// Confirm
	ok, err := mock.Confirm("Are you sure?", "")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = mock.Confirm("Are you sure again?", "")
	require.NoError(t, err)
	assert.False(t, ok)

	// Input
	val, err := mock.Input("Name", "", "default")
	require.NoError(t, err)
	assert.Equal(t, "article", val)

	val, err = mock.Input("Tag", "", "default")
	require.NoError(t, err)
	assert.Equal(t, "custom_tag", val)

	// Select
	sel, err := mock.Select("Scope", "", []string{"admin", "client"}, "admin")
	require.NoError(t, err)
	assert.Equal(t, "admin", sel)
}

func TestTestFactory_Customization(t *testing.T) {
	t.Parallel()

	streams, _, _, _ := core.TestIOStreams()
	mockPrompter := core.NewMockPrompter()
	cfg := &config.Config{}
	cfg.App.Name = "test-app"

	f := &console.TestFactory{
		StreamsValue:  streams,
		PrompterValue: mockPrompter,
		ConfigValue:   cfg,
	}

	assert.Equal(t, streams, f.IOStreams())
	assert.Equal(t, mockPrompter, f.Prompter())

	gotCfg, err := f.Config()
	require.NoError(t, err)
	assert.Equal(t, "test-app", gotCfg.App.Name)

	db, err := f.DB(context.Background())
	require.NoError(t, err)
	assert.Nil(t, db)

	// 测试自定义错误注入
	fErr := &console.TestFactory{
		ConfigErr: errors.New("load config failed"),
	}
	_, err = fErr.Config()
	assert.EqualError(t, err, "load config failed")
}
