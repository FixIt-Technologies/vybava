package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/FixIt-Technologies/vybava/internal/catalog"
)

type browserModel struct {
	catalog   catalog.Catalog
	cursor    int
	selected  map[int]struct{}
	confirmed bool
}

func Select(c catalog.Catalog) ([]string, bool, error) {
	selected := make(map[int]struct{})
	recommended, _ := c.Resolve([]string{"recommended"})
	for i, item := range c.Items {
		for _, defaultItem := range recommended {
			if item.ID == defaultItem.ID {
				selected[i] = struct{}{}
			}
		}
	}
	program := tea.NewProgram(browserModel{catalog: c, selected: selected})
	finalModel, err := program.Run()
	if err != nil {
		return nil, false, err
	}
	result, ok := finalModel.(browserModel)
	if !ok || !result.confirmed {
		return nil, false, nil
	}
	var ids []string
	for index, item := range result.catalog.Items {
		if _, exists := result.selected[index]; exists {
			ids = append(ids, item.ID)
		}
	}
	return ids, true, nil
}

func (m browserModel) Init() tea.Cmd { return nil }

func (m browserModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.KeyPressMsg:
		switch message.String() {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.catalog.Items)-1 {
				m.cursor++
			}
		case "space":
			if _, selected := m.selected[m.cursor]; selected {
				delete(m.selected, m.cursor)
			} else {
				m.selected[m.cursor] = struct{}{}
			}
		case "enter":
			m.confirmed = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m browserModel) View() tea.View {
	var view strings.Builder
	view.WriteString("Výbava — select packages to install\n\n")
	for index, item := range m.catalog.Items {
		cursor := " "
		if index == m.cursor {
			cursor = ">"
		}
		checked := " "
		if _, selected := m.selected[index]; selected {
			checked = "x"
		}
		groups := strings.Join(m.catalog.GroupIDsFor(item.ID), ", ")
		fmt.Fprintf(&view, "%s [%s] %-12s %-12s %s\n", cursor, checked, item.ID, item.Status, groups)
		if index == m.cursor {
			fmt.Fprintf(&view, "      %s\n", item.Description)
		}
	}
	view.WriteString("\n↑/↓ move  space toggle  enter install  q cancel\n")
	return tea.NewView(view.String())
}
