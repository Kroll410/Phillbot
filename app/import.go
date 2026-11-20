package main

// Імпорт даних з іншого файлу бази PhillBot (phillbot.db): з вебверсії (кілька каналів) або з іншого ПК.
// Береться один канал і цілком замінює дані цього каналу в поточній базі: стріми, збори, замовлення,
// аукціони, лоти, ставки, оброблені id і поточний стан. Токени, секрети й налаштування входу не копіюються.

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ImportChannel — канал у файлі, з якого можна імпортувати.
type ImportChannel struct {
	ID          string `json:"id"`
	Name        string `json:"name"` // нік Twitch, якщо відомий (у файлі вебверсії)
	Streams     int    `json:"streams"`
	Collections int    `json:"collections"`
	Entries     int    `json:"entries"`
	Auctions    int    `json:"auctions"`
	UpdatedAt   int64  `json:"updatedAt"`
}

// таблиці з даними каналу: колонка каналу в кожній
var importTables = []struct{ name, chCol string }{
	{"channels", "id"}, {"streams", "channel_id"}, {"collections", "channel_id"}, {"entries", "channel_id"},
	{"auctions", "channel_id"}, {"lots", "channel_id"}, {"bids", "channel_id"}, {"seen", "channel_id"},
}

func openImportSource(file string) (*sql.DB, error) {
	if st, err := os.Stat(file); err != nil {
		return nil, err
	} else if st.IsDir() {
		return nil, errors.New("це папка, а не файл бази")
	}
	db, err := sql.Open("sqlite", "file:"+sqliteURIPath(file)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'channels'`).Scan(&n); err != nil || n == 0 {
		db.Close()
		if err == nil {
			err = errors.New("це не база PhillBot (немає таблиці channels)")
		}
		return nil, err
	}
	return db, nil
}

func countWhere(db *sql.DB, table, col, ch string) int {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+col+` = ?`, ch).Scan(&n)
	return n
}

// listImportChannels — канали у файлі бази (найновіші зверху).
func listImportChannels(file string) ([]ImportChannel, error) {
	db, err := openImportSource(file)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	names := map[string]string{} // id Twitch → нік (таблиця входу вебверсії)
	if rows, err := db.Query(`SELECT COALESCE(twitch_id, ''), COALESCE(NULLIF(display_name, ''), login) FROM web_users`); err == nil {
		for rows.Next() {
			var id, name string
			if rows.Scan(&id, &name) == nil && id != "" {
				names[id] = name
			}
		}
		rows.Close()
	}
	rows, err := db.Query(`SELECT id, updated_at FROM channels`)
	if err != nil {
		return nil, err
	}
	var out []ImportChannel
	for rows.Next() {
		var c ImportChannel
		if rows.Scan(&c.ID, &c.UpdatedAt) == nil {
			out = append(out, c)
		}
	}
	rows.Close()
	for i := range out {
		c := &out[i]
		c.Name = names[c.ID]
		c.Streams = countWhere(db, "streams", "channel_id", c.ID)
		c.Collections = countWhere(db, "collections", "channel_id", c.ID)
		c.Entries = countWhere(db, "entries", "channel_id", c.ID)
		c.Auctions = countWhere(db, "auctions", "channel_id", c.ID)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

func tableCols(db *sql.DB, schema, table string) ([]string, error) {
	rows, err := db.Query(`PRAGMA ` + schema + `.table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

// backupTo — повна копія поточної бази у файл (перед заміною даних).
func (s *Store) backupTo(file string) error {
	if s.pg {
		return errors.New("резервна копія — лише для SQLite")
	}
	_, err := s.db.Exec(`VACUUM INTO ?`, file)
	return err
}

// importChannel — замінити дані каналу цієї бази даними каналу srcCh з файлу file.
// Лише для SQLite (десктоп). Після імпорту стан треба перечитати (Load).
func (s *Store) importChannel(file, srcCh string) error {
	if s.pg {
		return errors.New("імпорт підтримується лише для бази SQLite")
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return err
	}
	src, err := openImportSource(abs) // перевірка, що це база PhillBot і канал у ній є
	if err != nil {
		return err
	}
	found := countWhere(src, "channels", "id", srcCh) > 0
	src.Close()
	if !found {
		return fmt.Errorf("у файлі немає каналу %s", srcCh)
	}
	if _, err := s.db.Exec(`ATTACH DATABASE ? AS src`, "file:"+sqliteURIPath(abs)+"?mode=ro"); err != nil {
		return fmt.Errorf("не вдалося відкрити файл: %w", err)
	}
	defer s.db.Exec(`DETACH DATABASE src`)
	// запити готуємо до транзакції: у SQLite одне з'єднання, і всередині транзакції інших запитів не буде
	type plan struct{ del, ins string }
	var plans []plan
	for _, t := range importTables {
		p := plan{del: `DELETE FROM main.` + t.name + ` WHERE ` + t.chCol + ` = ?`}
		srcCols, err := tableCols(s.db, "src", t.name)
		if err != nil {
			return err
		}
		if len(srcCols) > 0 { // таблиця є у джерелі (у старих версіях бази деяких немає)
			dst, err := tableCols(s.db, "main", t.name)
			if err != nil {
				return err
			}
			inSrc := map[string]bool{}
			for _, c := range srcCols {
				inSrc[c] = true
			}
			var cols, sel []string
			for _, c := range dst {
				if !inSrc[c] {
					continue // колонки, якої немає у джерелі, — значення за замовчуванням
				}
				cols = append(cols, c)
				if c == t.chCol {
					sel = append(sel, "?")
				} else {
					sel = append(sel, c)
				}
			}
			p.ins = `INSERT INTO main.` + t.name + ` (` + strings.Join(cols, ", ") + `) SELECT ` + strings.Join(sel, ", ") +
				` FROM src.` + t.name + ` WHERE ` + t.chCol + ` = ?`
		}
		plans = append(plans, p)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, p := range plans {
		if _, err := tx.Exec(p.del, s.ch); err != nil {
			return fmt.Errorf("%s: %w", importTables[i].name, err)
		}
		if p.ins == "" {
			continue
		}
		if _, err := tx.Exec(p.ins, s.ch, srcCh); err != nil {
			return fmt.Errorf("%s: %w", importTables[i].name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.resetCache()
	return nil
}

// налаштування, що належать цьому ПК, а не каналу: їх імпорт не змінює
func keepLocalSettings(imported *Settings, local Settings) {
	imported.ObsPort = local.ObsPort
	imported.Chat = local.Chat
	imported.NgrokToken, imported.NgrokDomain, imported.DonKey = local.NgrokToken, local.NgrokDomain, local.DonKey
}

// importBackupName — файл резервної копії поруч із базою: phillbot.before-import-2026-11-20_14-05-00.db
func importBackupName(dir string, t time.Time) string {
	return filepath.Join(dir, "phillbot.before-import-"+t.Format("2006-01-02_15-04-05")+".db")
}
