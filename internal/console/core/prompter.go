package core

import (
	"errors"
	"fmt"

	"charm.land/huh/v2"
)

// Prompter 抽象控制台的人机交互提问契约 (DIP & ISP)。
type Prompter interface {
	Confirm(title, description string, affirmative ...string) (bool, error)
	Input(title, description, defaultValue string) (string, error)
	Select(title, description string, options []string, defaultOption string) (string, error)
}

// HuhPrompter 基于 charm.land/huh 的终端表单交互实现。
type HuhPrompter struct {
	canPrompt func() bool
}

// NewHuhPrompter 构造终端表单交互实现。
func NewHuhPrompter(canPrompt func() bool) *HuhPrompter {
	if canPrompt == nil {
		canPrompt = IsInteractive
	}
	return &HuhPrompter{canPrompt: canPrompt}
}

// Confirm 弹出交互式确认框。非交互环境下要求显式传入 --force 或返回错误。
func (p *HuhPrompter) Confirm(title, description string, affirmative ...string) (bool, error) {
	if !p.canPrompt() {
		return false, errors.New("confirmation required for destructive operation: please run interactively or specify --force")
	}

	aff := "Yes, continue"
	if len(affirmative) > 0 && affirmative[0] != "" {
		aff = affirmative[0]
	}

	var confirmed bool
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(title).
			Description(description).
			Affirmative(aff).
			Negative("Cancel").
			Value(&confirmed),
	))

	if err := form.Run(); err != nil {
		return false, fmt.Errorf("failed to read confirmation: %w", err)
	}
	return confirmed, nil
}

// Input 弹出文本输入框。
func (p *HuhPrompter) Input(title, description, defaultValue string) (string, error) {
	if !p.canPrompt() {
		return defaultValue, nil
	}

	val := defaultValue
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title(title).
			Description(description).
			Value(&val),
	))

	if err := form.Run(); err != nil {
		return "", fmt.Errorf("failed to read input: %w", err)
	}
	return val, nil
}

// Select 弹出单选列表。
func (p *HuhPrompter) Select(title, description string, options []string, defaultOption string) (string, error) {
	if !p.canPrompt() {
		return defaultOption, nil
	}

	huhOptions := make([]huh.Option[string], 0, len(options))
	for _, opt := range options {
		huhOptions = append(huhOptions, huh.NewOption(opt, opt))
	}

	selected := defaultOption
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(title).
			Description(description).
			Options(huhOptions...).
			Value(&selected),
	))

	if err := form.Run(); err != nil {
		return "", fmt.Errorf("failed to read selection: %w", err)
	}
	return selected, nil
}

// MockPrompter 供纯内存单元测试使用的 Prompter Mock 实现。
type MockPrompter struct {
	ConfirmResponses []bool
	InputResponses   []string
	SelectResponses  []string

	confirmIdx int
	inputIdx   int
	selectIdx  int
}

// NewMockPrompter 构造测试用 Prompter。
func NewMockPrompter() *MockPrompter {
	return &MockPrompter{}
}

func (m *MockPrompter) Confirm(_, _ string, _ ...string) (bool, error) {
	if m.confirmIdx < len(m.ConfirmResponses) {
		res := m.ConfirmResponses[m.confirmIdx]
		m.confirmIdx++
		return res, nil
	}
	return false, nil
}

func (m *MockPrompter) Input(_, _, defaultValue string) (string, error) {
	if m.inputIdx < len(m.InputResponses) {
		res := m.InputResponses[m.inputIdx]
		m.inputIdx++
		return res, nil
	}
	return defaultValue, nil
}

func (m *MockPrompter) Select(_, _ string, _ []string, defaultOption string) (string, error) {
	if m.selectIdx < len(m.SelectResponses) {
		res := m.SelectResponses[m.selectIdx]
		m.selectIdx++
		return res, nil
	}
	return defaultOption, nil
}
