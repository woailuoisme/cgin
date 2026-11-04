package console_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/console"
)

func TestIOStreams_BufferingAndColor(t *testing.T) {
	t.Parallel()

	streams, in, out, errOut := console.TestIOStreams()
	require.NotNil(t, streams)
	assert.False(t, streams.CanPrompt())
	assert.False(t, streams.ColorEnabled())

	_, err := fmt.Fprint(streams.Out, "hello stdout")
	require.NoError(t, err)
	assert.Equal(t, "hello stdout", out.String())

	_, err = fmt.Fprint(streams.ErrOut, "warning stderr")
	require.NoError(t, err)
	assert.Equal(t, "warning stderr", errOut.String())

	in.WriteString("hello\n")
	var readStr string
	_, err = fmt.Fscanln(streams.In, &readStr)
	require.NoError(t, err)
	assert.Equal(t, "hello", readStr)

	streams.SetColorEnabled(true)
	assert.True(t, streams.ColorEnabled())
}

func newRootForHelp(t *testing.T, color bool) *cobra.Command {
	t.Helper()

	prev := console.IsColorEnabled()
	t.Cleanup(func() { console.SetColorEnabled(prev) })

	cmd := console.NewRootCommand()
	require.NotNil(t, cmd)
	console.SetColorEnabled(color)
	return cmd
}

func getHelpOutput(t *testing.T, color bool, args ...string) string {
	t.Helper()
	cmd := newRootForHelp(t, color)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(append(args, "--help"))

	_ = cmd.ExecuteContext(context.Background())
	return buf.String()
}

func TestColoredHelp_Formatting(t *testing.T) {
	t.Run("plain text mode preserves alignment", func(t *testing.T) {
		output := getHelpOutput(t, false)
		assert.NotContains(t, output, "\x1b[")
		assert.Contains(t, output, "Usage:")
		assert.Contains(t, output, "cgin [command]")
		assert.Contains(t, output, "Available commands:")
		assert.Contains(t, output, "completion")
		assert.Contains(t, output, "help")
		assert.Contains(t, output, "serve")
		assert.Contains(t, output, "db\n")
		assert.Contains(t, output, "db:migrate")
		assert.Contains(t, output, "queue\n")
		assert.Contains(t, output, "queue:work")
		assert.Contains(t, output, "route\n")
		assert.Contains(t, output, "route:list")
		assert.Contains(t, output, "Flags:")
		assert.Contains(t, output, "-h, --help      help for cgin")
		assert.Contains(t, output, "-v, --version   version for cgin")
		assert.Contains(t, output, `Use "cgin [command] --help" for more information about a command.`)
	})

	t.Run("colored mode renders ANSI styles and preserves alignment", func(t *testing.T) {
		output := getHelpOutput(t, true)
		assert.Contains(t, output, "\x1b[")

		assert.Contains(t, output, console.StyleHeader("Usage:"))
		assert.Contains(t, output, console.StyleHeader("Available commands:"))
		assert.Contains(t, output, console.StyleHeader("db"))
		assert.Contains(t, output, console.StyleHeader("jwt"))
		assert.Contains(t, output, console.StyleHeader("make"))
		assert.Contains(t, output, console.StyleHeader("queue"))
		assert.Contains(t, output, console.StyleHeader("route"))
		assert.Contains(t, output, console.StyleHeader("scout"))
		assert.Contains(t, output, console.StyleHeader("Flags:"))

		rootCmd := newRootForHelp(t, true)
		pad := rootCmd.NamePadding()

		assert.Contains(t, output, console.StyleCmd("serve", pad))
		assert.Contains(t, output, console.StyleCmd("db:migrate", pad))
		assert.Contains(t, output, console.StyleCmd("queue:work", pad))
		assert.Contains(t, output, console.StyleCmd("route:list", pad))
		assert.Contains(t, output, console.StyleCmd("completion", pad))
		assert.Contains(t, output, console.StyleCmd("help", pad))

		assert.Contains(t, output, "help for cgin")
		assert.Contains(t, output, "version for cgin")
		assert.Contains(t, output, `Use "`+console.StyleUseLine("cgin [command] --help")+`" for more information about a command.`)
	})

	t.Run("flag styling preserves column alignment", func(t *testing.T) {
		rawFlags := `  -h, --help             help for cgin
  -p, --port int         Override APP_PORT
      --chinese string   Chinese desc
      --client           Client flag
`
		console.SetColorEnabled(false)
		plain := console.StyleFlags(rawFlags)
		assert.Equal(t, rawFlags, plain)

		console.SetColorEnabled(true)
		colored := console.StyleFlags(rawFlags)
		assert.Contains(t, colored, "\x1b[")

		plainLines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
		coloredLines := strings.Split(strings.TrimRight(colored, "\n"), "\n")
		require.Len(t, coloredLines, len(plainLines))
	})

	t.Run("subcommand help inherits colored template", func(t *testing.T) {
		serveOutput := getHelpOutput(t, true, "serve")
		assert.Contains(t, serveOutput, console.StyleHeader("Usage:"))
		assert.Contains(t, serveOutput, console.StyleHeader("Flags:"))
		assert.Contains(t, serveOutput, console.StyleUseLine("cgin serve [flags]"))
		assert.Contains(t, serveOutput, "Override APP_PORT")

		crudOutput := getHelpOutput(t, true, "make:crud")
		assert.Contains(t, crudOutput, console.StyleHeader("Usage:"))
		assert.Contains(t, crudOutput, console.StyleHeader("Flags:"))
		assert.Contains(t, crudOutput, console.StyleUseLine("cgin make:crud [name] [flags]"))
		assert.Contains(t, crudOutput, "Custom Chinese business description")
	})
}

func TestProgressBar_Interactive_Records(t *testing.T) {
	buf := new(bytes.Buffer)
	bar := console.NewProgressBarTo(buf, true, 1000, "Processing")

	bar.Set(250)
	bar.Set(500)
	bar.Set(1000)
	bar.Finish()

	out := buf.String()
	assert.Contains(t, out, "Processing")
	assert.Contains(t, out, "1,000")
	assert.Contains(t, out, "/s")
}

func TestProgressBar_NonInteractive_Milestones(t *testing.T) {
	buf := new(bytes.Buffer)
	bar := console.NewProgressBarTo(buf, false, 100, "Exporting")

	bar.Set(10)
	assert.Empty(t, buf.String())

	bar.Set(25)
	assert.Contains(t, buf.String(), "25%")

	bar.Set(50)
	assert.Contains(t, buf.String(), "50%")

	bar.Set(100)
	bar.Finish()
	assert.Contains(t, buf.String(), "100%")
}

func TestProgressBar_ByteMode(t *testing.T) {
	buf := new(bytes.Buffer)
	totalBytes := int64(10 * 1024 * 1024)
	bar := console.NewByteProgressBarTo(buf, true, totalBytes, "Downloading")

	bar.Set(5 * 1024 * 1024)
	time.Sleep(10 * time.Millisecond)
	bar.Set(totalBytes)
	bar.Finish()

	out := buf.String()
	assert.Contains(t, out, "Downloading")
	assert.Contains(t, out, "MB")
	assert.Contains(t, out, "/s")
}

func TestProgressBar_UnknownTotal(t *testing.T) {
	buf := new(bytes.Buffer)
	bar := console.NewByteProgressBarTo(buf, true, -1, "Streaming")

	bar.Set(2 * 1024 * 1024)
	bar.Finish()

	out := buf.String()
	assert.Contains(t, out, "Streaming")
	assert.Contains(t, out, "2.1 MB")
	assert.Contains(t, out, "/s")
}

func TestProgressBar_NarrowTerminal(t *testing.T) {
	buf := new(bytes.Buffer)
	totalBytes := int64(10 * 1024 * 1024)
	bar := console.NewByteProgressBarTo(buf, true, totalBytes, "Downloading")
	bar.SetTermWidth(48)

	bar.Set(5 * 1024 * 1024)
	bar.Finish()

	out := buf.String()
	assert.Contains(t, out, "50%")
	assert.Contains(t, out, "5.2MB/10MB")
	assert.Contains(t, out, "/s")
	assert.Contains(t, out, "\x1b[")
}
