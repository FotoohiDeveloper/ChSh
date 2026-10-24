package service

import (
	"context"
	"time"

	"github.com/xuri/excelize/v2"

	"chador/internal/domain"
)

type PlaceStat struct {
	PlaceID      int16          `json:"place_id"`
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	ByStatus     map[string]int `json:"by_status"`
	SocksBalance int            `json:"socks_balance"`
	OutFromHere  int            `json:"tents_out_from_here"`
}

type Dashboard struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Scope       string         `json:"scope"`
	Totals      map[string]int `json:"totals"`
	Places      []PlaceStat    `json:"places"`
	Today       struct {
		Assigned int `json:"assigned"`
		Returned int `json:"returned"`
	} `json:"today"`
	Overdue int `json:"overdue"`
}

// Dashboard is computed once per two seconds no matter how many screens are
// open (coalesced + cached); gate-level roles only see their own place.
func (s *Service) Dashboard(ctx context.Context, a Actor) (*Dashboard, error) {
	all := a.Can(domain.PermDashboardAll)
	if !all && !a.Can(domain.PermDashboardOwn) {
		return nil, Forbidden("دسترسی لازم برای این کار را ندارید")
	}
	full, err := s.dash.Get("all", func() (*Dashboard, error) { return s.buildDashboard(context.WithoutCancel(ctx)) })
	if err != nil {
		return nil, err
	}
	if all {
		return full, nil
	}
	if a.PlaceID == nil {
		return nil, ErrNeedShift
	}
	own := &Dashboard{GeneratedAt: full.GeneratedAt, Scope: "place", Totals: map[string]int{}, Places: []PlaceStat{}}
	for _, p := range full.Places {
		if p.PlaceID == *a.PlaceID {
			own.Places = append(own.Places, p)
			for k, v := range p.ByStatus {
				own.Totals[k] = v
			}
		}
	}
	return own, nil
}

func (s *Service) buildDashboard(ctx context.Context) (*Dashboard, error) {
	d := &Dashboard{GeneratedAt: time.Now(), Scope: "all", Totals: map[string]int{}}
	rows, err := s.db.Query(ctx, `SELECT id, name, kind::text FROM places WHERE active ORDER BY id`)
	if err != nil {
		return nil, err
	}
	idx := map[int16]int{}
	for rows.Next() {
		var p PlaceStat
		if err := rows.Scan(&p.PlaceID, &p.Name, &p.Kind); err != nil {
			rows.Close()
			return nil, err
		}
		p.ByStatus = map[string]int{}
		idx[p.PlaceID] = len(d.Places)
		d.Places = append(d.Places, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	trows, err := s.db.Query(ctx, `SELECT place_id, status::text, count(*)::int FROM tents GROUP BY 1,2`)
	if err != nil {
		return nil, err
	}
	for trows.Next() {
		var place *int16
		var st string
		var n int
		if err := trows.Scan(&place, &st, &n); err != nil {
			trows.Close()
			return nil, err
		}
		d.Totals[st] += n
		if place != nil {
			if i, ok := idx[*place]; ok {
				d.Places[i].ByStatus[st] += n
			}
		}
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return nil, err
	}

	srows, err := s.db.Query(ctx, `SELECT place_id, COALESCE(sum(CASE kind WHEN 'supply' THEN qty ELSE -qty END),0)::int FROM socks_ledger WHERE place_id IS NOT NULL GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	for srows.Next() {
		var place int16
		var n int
		if err := srows.Scan(&place, &n); err != nil {
			srows.Close()
			return nil, err
		}
		if i, ok := idx[place]; ok {
			d.Places[i].SocksBalance = n
		}
	}
	srows.Close()

	orows, err := s.db.Query(ctx, `SELECT assigned_place_id, count(*)::int FROM assignments
		WHERE returned_at IS NULL AND cancelled_at IS NULL AND lost_at IS NULL AND assigned_place_id IS NOT NULL GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	for orows.Next() {
		var place int16
		var n int
		if err := orows.Scan(&place, &n); err != nil {
			orows.Close()
			return nil, err
		}
		if i, ok := idx[place]; ok {
			d.Places[i].OutFromHere = n
		}
	}
	orows.Close()

	start := dayStart(time.Now(), s.cfg.Location)
	if err := s.db.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM assignments WHERE assigned_at >= $1 AND cancelled_at IS NULL)::int,
		(SELECT count(*) FROM assignments WHERE returned_at >= $1)::int,
		(SELECT count(*) FROM assignments WHERE returned_at IS NULL AND cancelled_at IS NULL AND lost_at IS NULL AND assigned_at < now() - make_interval(secs => $2))::int`,
		start, s.cfg.ReminderAfter.Seconds()).Scan(&d.Today.Assigned, &d.Today.Returned, &d.Overdue); err != nil {
		return nil, err
	}
	return d, nil
}

func dayStart(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// ---- daily report ----

type PlaceDay struct {
	Name       string `json:"name"`
	Assigned   int    `json:"assigned"`
	Returned   int    `json:"returned"`
	SocksQty   int    `json:"socks_qty"`
	SocksMoney int64  `json:"socks_amount"`
}

type OverdueRow struct {
	Code       string    `json:"code"`
	AssignedAt time.Time `json:"assigned_at"`
	Place      string    `json:"place"`
	Reminders  int       `json:"reminders"`
}

type DailyReport struct {
	Date      string         `json:"date"`
	Assigned  int            `json:"assigned"`
	Returned  int            `json:"returned"`
	Cancelled int            `json:"cancelled"`
	Lost      int            `json:"lost"`
	SocksQty  int            `json:"socks_qty"`
	SocksSum  int64          `json:"socks_amount"`
	Places    []PlaceDay     `json:"places"`
	Status    map[string]int `json:"status_now"`
	Overdue   []OverdueRow   `json:"overdue"`
}

func (s *Service) DailyReport(ctx context.Context, a Actor, date string) (*DailyReport, error) {
	if err := a.require(domain.PermReports); err != nil {
		return nil, err
	}
	day, err := time.ParseInLocation("2006-01-02", date, s.cfg.Location)
	if err != nil {
		return nil, BadRequest("bad_date", "تاریخ را به صورت YYYY-MM-DD وارد کنید")
	}
	from, to := day, day.AddDate(0, 0, 1)
	r := &DailyReport{Date: date, Places: []PlaceDay{}, Status: map[string]int{}, Overdue: []OverdueRow{}}

	if err := s.db.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM assignments WHERE assigned_at >= $1 AND assigned_at < $2 AND cancelled_at IS NULL)::int,
		(SELECT count(*) FROM assignments WHERE returned_at >= $1 AND returned_at < $2)::int,
		(SELECT count(*) FROM assignments WHERE cancelled_at >= $1 AND cancelled_at < $2)::int,
		(SELECT count(*) FROM assignments WHERE lost_at >= $1 AND lost_at < $2)::int,
		(SELECT COALESCE(sum(qty),0) FROM socks_ledger WHERE kind='issue' AND created_at >= $1 AND created_at < $2)::int,
		(SELECT COALESCE(sum(amount),0) FROM socks_ledger WHERE kind='issue' AND created_at >= $1 AND created_at < $2)::bigint`,
		from, to).Scan(&r.Assigned, &r.Returned, &r.Cancelled, &r.Lost, &r.SocksQty, &r.SocksSum); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(ctx, `SELECT p.name,
		(SELECT count(*) FROM assignments WHERE assigned_place_id=p.id AND assigned_at >= $1 AND assigned_at < $2 AND cancelled_at IS NULL)::int,
		(SELECT count(*) FROM assignments WHERE returned_place_id=p.id AND returned_at >= $1 AND returned_at < $2)::int,
		(SELECT COALESCE(sum(qty),0) FROM socks_ledger WHERE place_id=p.id AND kind='issue' AND created_at >= $1 AND created_at < $2)::int,
		(SELECT COALESCE(sum(amount),0) FROM socks_ledger WHERE place_id=p.id AND kind='issue' AND created_at >= $1 AND created_at < $2)::bigint
		FROM places p WHERE p.active AND p.kind IN ('gate','storage') ORDER BY p.id`, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p PlaceDay
		if err := rows.Scan(&p.Name, &p.Assigned, &p.Returned, &p.SocksQty, &p.SocksMoney); err != nil {
			rows.Close()
			return nil, err
		}
		r.Places = append(r.Places, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	srows, err := s.db.Query(ctx, `SELECT status::text, count(*)::int FROM tents GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	for srows.Next() {
		var st string
		var n int
		if err := srows.Scan(&st, &n); err != nil {
			srows.Close()
			return nil, err
		}
		r.Status[st] = n
	}
	srows.Close()

	orows, err := s.db.Query(ctx, `SELECT t.code, a.assigned_at, COALESCE(p.name,''), a.reminders FROM assignments a
		JOIN tents t ON t.id=a.tent_id LEFT JOIN places p ON p.id=a.assigned_place_id
		WHERE a.returned_at IS NULL AND a.cancelled_at IS NULL AND a.lost_at IS NULL
		AND a.assigned_at < now() - make_interval(secs => $1) ORDER BY a.assigned_at LIMIT 2000`, s.cfg.ReminderAfter.Seconds())
	if err != nil {
		return nil, err
	}
	for orows.Next() {
		var o OverdueRow
		if err := orows.Scan(&o.Code, &o.AssignedAt, &o.Place, &o.Reminders); err != nil {
			orows.Close()
			return nil, err
		}
		r.Overdue = append(r.Overdue, o)
	}
	orows.Close()
	return r, orows.Err()
}

var statusFa = map[string]string{
	"ready": "آماده", "with_pilgrim": "نزد زائر", "dirty": "کثیف", "in_wash": "در کارگاه",
	"pending_review": "در انتظار بررسی", "repair": "تعمیر", "retired": "خارج از چرخه",
}

// Excel renders the report as a right-to-left workbook.
func (s *Service) DailyReportXLSX(r *DailyReport) (*excelize.File, error) {
	f := excelize.NewFile()
	rtl := true
	sheets := []string{"خلاصه", "به تفکیک گیت", "عودت‌نشده"}
	f.SetSheetName("Sheet1", sheets[0])
	for _, n := range sheets[1:] {
		if _, err := f.NewSheet(n); err != nil {
			return nil, err
		}
	}
	for _, n := range sheets {
		_ = f.SetSheetView(n, 0, &excelize.ViewOptions{RightToLeft: &rtl})
	}
	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, Fill: excelize.Fill{Type: "pattern", Color: []string{"#E8EEF7"}, Pattern: 1}})
	put := func(sheet string, row int, vals ...any) {
		for i, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(i+1, row)
			_ = f.SetCellValue(sheet, cell, v)
		}
	}
	put(sheets[0], 1, "عنوان", "مقدار")
	put(sheets[0], 2, "تاریخ", r.Date)
	put(sheets[0], 3, "چادر تحویل‌شده", r.Assigned)
	put(sheets[0], 4, "چادر عودت‌شده", r.Returned)
	put(sheets[0], 5, "تخصیص لغوشده", r.Cancelled)
	put(sheets[0], 6, "چادر مفقود", r.Lost)
	put(sheets[0], 7, "جوراب (تعداد)", r.SocksQty)
	put(sheets[0], 8, "جوراب (مبلغ)", r.SocksSum)
	row := 10
	put(sheets[0], row, "وضعیت فعلی چادرها", "تعداد")
	_ = f.SetCellStyle(sheets[0], "A1", "B1", bold)
	_ = f.SetCellStyle(sheets[0], "A10", "B10", bold)
	for _, st := range []string{"ready", "with_pilgrim", "dirty", "in_wash", "pending_review", "repair", "retired"} {
		row++
		put(sheets[0], row, statusFa[st], r.Status[st])
	}
	put(sheets[1], 1, "گیت/محل", "تحویل", "عودت", "جوراب (تعداد)", "جوراب (مبلغ)")
	_ = f.SetCellStyle(sheets[1], "A1", "E1", bold)
	for i, p := range r.Places {
		put(sheets[1], i+2, p.Name, p.Assigned, p.Returned, p.SocksQty, p.SocksMoney)
	}
	put(sheets[2], 1, "کد چادر", "زمان تحویل", "گیت", "تعداد یادآوری")
	_ = f.SetCellStyle(sheets[2], "A1", "D1", bold)
	for i, o := range r.Overdue {
		put(sheets[2], i+2, o.Code, o.AssignedAt.In(s.cfg.Location).Format("2006-01-02 15:04"), o.Place, o.Reminders)
	}
	for _, n := range sheets {
		_ = f.SetColWidth(n, "A", "E", 22)
	}
	return f, nil
}
