package main

// Кожен «Старт» — окремий збір. Після завершення збір автоматично зберігається:
//   exports/subday_<дата-час старту>.csv — таблиця для Excel (UTF-8 з BOM, роздільник «;»);
//   history/subday_<…>.json — повна копія (налаштування, таймер, замовлення).
// Перед новим «Старт» обидва файли перезаписуються востаннє (з правками, зробленими після завершення).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (a *App) collectionName(ext string) string {
	t := time.UnixMilli(a.st.Session.StartedAt)
	if a.st.Session.StartedAt == 0 {
		t = time.Now()
	}
	return "subday_" + t.Format("2006-01-02_15-04-05") + ext
}

func csvCell(v string) string {
	if strings.ContainsAny(v, "\";\r\n") {
		return "\"" + strings.ReplaceAll(v, "\"", "\"\"") + "\""
	}
	return v
}

func collectionCSV(s Settings, entries []Entry) string {
	var b strings.Builder
	line := func(cells ...string) {
		for i, c := range cells {
			if i > 0 {
				b.WriteString(";")
			}
			b.WriteString(csvCell(c))
		}
		b.WriteString("\r\n")
	}
	b.WriteString("sep=;\r\n")
	counted := countedIDs(s, entries)
	line("Час", "Чатерс", "Логін", "Тір", "Місяці", "Бали", "Замовлення", "Рахується")
	for _, e := range entries {
		tier, months := "—", ""
		if e.Tier > 0 {
			tier, months = strconv.Itoa(e.Tier), strconv.Itoa(e.Months)
		}
		status := "так"
		if e.Excluded {
			status = "приховано"
		} else if !counted[e.ID] {
			status = "ні (дубль)"
		}
		line(time.UnixMilli(e.Ts).Format("2006-01-02 15:04:05"), e.User, e.Login, tier, months, strconv.Itoa(pointsOf(s, e)), e.Text, status)
	}
	b.WriteString("\r\nПідсумок\r\n")
	line("Замовлення", "Бали", "Людей", "Хто")
	for _, g := range aggregate(s, entries) {
		var who []string
		for _, u := range g.Users {
			who = append(who, u.Name+" ("+strconv.Itoa(u.P)+")")
		}
		line(g.Name, strconv.Itoa(g.Points), strconv.Itoa(g.Count), strings.Join(who, ", "))
	}
	return b.String()
}

// CSV поточного збору в exports/. Викликати під a.mu. Порожній збір не зберігається.
func (a *App) writeCSVLocked() string {
	if len(a.st.Entries) == 0 {
		return ""
	}
	if err := os.MkdirAll(a.exportsDir, 0755); err != nil {
		return ""
	}
	p := filepath.Join(a.exportsDir, a.collectionName(".csv"))
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte(collectionCSV(a.st.Settings, a.st.Entries))...)
	if err := os.WriteFile(p, data, 0644); err != nil {
		go a.logf("Не вдалося записати %s — можливо, файл відкритий в Excel", p)
		return ""
	}
	return p
}

// Остаточно зберегти поточний збір (CSV + JSON) перед очищенням. Викликати під a.mu.
func (a *App) archiveLocked() string {
	if len(a.st.Entries) == 0 {
		return ""
	}
	csv := a.writeCSVLocked()
	if err := os.MkdirAll(a.historyDir, 0755); err == nil {
		b, _ := json.MarshalIndent(map[string]any{"archivedAt": time.Now().Format(time.RFC3339), "settings": a.st.Settings,
			"session": a.st.Session, "entries": a.st.Entries}, "", "  ")
		_ = writeFileAtomic(filepath.Join(a.historyDir, a.collectionName(".json")), b)
	}
	return csv
}
