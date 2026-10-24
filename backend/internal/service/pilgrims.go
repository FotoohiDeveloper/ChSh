package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"chador/internal/domain"
)

type PilgrimInput struct {
	NationalID string `json:"national_id"`
	BirthDate  string `json:"birth_date"`
	Mobile     string `json:"mobile"`
}

type PilgrimView struct {
	ID           uuid.UUID `json:"id"`
	Verification string    `json:"verification"`
	FullName     string    `json:"full_name,omitempty"`
	NationalID   string    `json:"national_id_masked"`
	Mobile       string    `json:"mobile_masked"`
	ActiveTents  []string  `json:"active_tents"`
	Existing     bool      `json:"existing"`
	// Usable: tents may be handed out to this pilgrim (verified, or unverified while ALLOW_UNVERIFIED=true).
	Usable bool `json:"usable"`
}

// finish fills Usable and, for an unverified pilgrim without a name, a temporary display name
// derived from the last digits of the national ID (not stored).
func (s *Service) finish(v *PilgrimView, nid string) *PilgrimView {
	v.Usable = v.Verification == "verified" || (v.Verification != "failed" && s.cfg.AllowUnverified)
	if v.FullName == "" && v.Verification != "verified" && len(nid) >= 4 {
		v.FullName = "زائر موقت " + nid[len(nid)-4:]
	}
	return v
}

type pilgrimRow struct {
	id           uuid.UUID
	nidEnc       []byte
	mobileEnc    []byte
	nameEnc      []byte
	verification string
}

// UpsertPilgrim registers a pilgrim (or finds the existing record), checks the
// identity with the inquiry service and returns a masked view. Personal data
// is encrypted before it touches the database.
func (s *Service) UpsertPilgrim(ctx context.Context, a Actor, in PilgrimInput) (*PilgrimView, error) {
	if err := a.require(domain.PermPilgrimWrite); err != nil {
		return nil, err
	}
	nid := domain.NormalizeDigits(in.NationalID)
	if !domain.ValidNationalID(nid) {
		return nil, Unprocessable("bad_national_id", "کد ملی نامعتبر است")
	}
	mobile, err := domain.NormalizeMobile(in.Mobile)
	if err != nil {
		return nil, Unprocessable("bad_mobile", "شماره موبایل نامعتبر است")
	}
	birth, err := domain.ParseBirthDate(in.BirthDate)
	if err != nil {
		return nil, Unprocessable("bad_birth_date", "تاریخ تولد نامعتبر است")
	}
	nidIdx := s.crypto.Index("nid", nid)
	mobileIdx := s.crypto.Index("mobile", mobile)

	// A returning pilgrim: no new inquiry needed if already verified.
	var row pilgrimRow
	err = s.db.QueryRow(ctx, `SELECT id, nid_enc, mobile_enc, name_enc, verification::text FROM pilgrims WHERE nid_idx=$1 AND anonymized_at IS NULL`, nidIdx).
		Scan(&row.id, &row.nidEnc, &row.mobileEnc, &row.nameEnc, &row.verification)
	if err == nil {
		return s.refreshExisting(ctx, a, row, nid, mobile, mobileIdx, birth)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	res, ierr := s.inq.Verify(ctx, nid, birth, mobile)
	state := "pending"
	switch {
	case ierr == nil && !res.Verified:
		// The service answered and the identity does not match: store nothing.
		s.AuditNow(ctx, &a.UserID, a.IP, "pilgrim.identity_mismatch", "pilgrim", "", nil)
		return nil, Unprocessable("identity_mismatch", "اطلاعات وارد‌شده با ثبت احوال مطابقت ندارد")
	case ierr == nil:
		state = "verified"
	default:
		s.log.Warn("identity inquiry unavailable", "err", ierr)
		if !s.cfg.AllowUnverified {
			// Keep the record so the retry job can verify it, but it cannot be used yet.
			state = "pending"
		}
	}

	nidEnc, _ := s.crypto.Seal("pilgrim.nid", nid)
	birthEnc, _ := s.crypto.Seal("pilgrim.birth", birth.Format("2006-01-02"))
	mobEnc, _ := s.crypto.Seal("pilgrim.mobile", mobile)
	var nameEnc []byte
	if res.FullName != "" {
		nameEnc, _ = s.crypto.Seal("pilgrim.name", res.FullName)
	}
	var id uuid.UUID
	err = s.db.QueryRow(ctx, `INSERT INTO pilgrims(nid_enc, nid_idx, birth_enc, mobile_enc, mobile_idx, name_enc, verification, verified_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7::verification, CASE WHEN $7='verified' THEN now() END, $8)
		ON CONFLICT (nid_idx) DO NOTHING RETURNING id`,
		nidEnc, nidIdx, birthEnc, mobEnc, mobileIdx, nameEnc, state, a.UserID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) { // another gate registered the same person at the same moment
		return s.UpsertPilgrim(ctx, a, in)
	}
	if err != nil {
		return nil, err
	}
	if state == "pending" {
		_ = s.q.VerifyPilgrim(ctx, id, 30*time.Second)
	}
	s.Audit(a, "pilgrim.create", "pilgrim", id.String(), map[string]any{"verification": state})
	return s.finish(&PilgrimView{ID: id, Verification: state, FullName: res.FullName,
		NationalID: domain.MaskNationalID(nid), Mobile: domain.MaskMobile(mobile), ActiveTents: []string{}}, nid), nil
}

func (s *Service) refreshExisting(ctx context.Context, a Actor, row pilgrimRow, nid, mobile string, mobileIdx []byte, birth time.Time) (*PilgrimView, error) {
	oldMobile, _ := s.crypto.Open("pilgrim.mobile", row.mobileEnc)
	if oldMobile != mobile {
		enc, _ := s.crypto.Seal("pilgrim.mobile", mobile)
		if _, err := s.db.Exec(ctx, `UPDATE pilgrims SET mobile_enc=$2, mobile_idx=$3, last_seen_at=now() WHERE id=$1`, row.id, enc, mobileIdx); err != nil {
			return nil, err
		}
	} else if _, err := s.db.Exec(ctx, `UPDATE pilgrims SET last_seen_at=now() WHERE id=$1`, row.id); err != nil {
		return nil, err
	}
	if row.verification != "verified" {
		if ok, err := s.verifyNow(ctx, row.id, nid, birth, mobile); err == nil && ok {
			row.verification = "verified"
		}
	}
	v := &PilgrimView{ID: row.id, Verification: row.verification, NationalID: domain.MaskNationalID(nid),
		Mobile: domain.MaskMobile(mobile), Existing: true}
	if row.nameEnc != nil {
		v.FullName, _ = s.crypto.Open("pilgrim.name", row.nameEnc)
	}
	tents, err := s.activeTents(ctx, row.id)
	if err != nil {
		return nil, err
	}
	v.ActiveTents = tents
	s.finish(v, nid)
	s.Audit(a, "pilgrim.view", "pilgrim", row.id.String(), nil)
	return v, nil
}

func (s *Service) verifyNow(ctx context.Context, id uuid.UUID, nid string, birth time.Time, mobile string) (bool, error) {
	res, err := s.inq.Verify(ctx, nid, birth, mobile)
	if err != nil {
		return false, err
	}
	if !res.Verified {
		_, err = s.db.Exec(ctx, `UPDATE pilgrims SET verification='failed' WHERE id=$1`, id)
		return false, err
	}
	var nameEnc []byte
	if res.FullName != "" {
		nameEnc, _ = s.crypto.Seal("pilgrim.name", res.FullName)
	}
	_, err = s.db.Exec(ctx, `UPDATE pilgrims SET verification='verified', verified_at=now(), name_enc=COALESCE($2, name_enc) WHERE id=$1`, id, nameEnc)
	return err == nil, err
}

// VerifyPending is called by the retry job when the inquiry service was down.
func (s *Service) VerifyPending(ctx context.Context, id uuid.UUID) error {
	var nidEnc, birthEnc, mobEnc []byte
	var state string
	err := s.db.QueryRow(ctx, `SELECT nid_enc, birth_enc, mobile_enc, verification::text FROM pilgrims WHERE id=$1 AND anonymized_at IS NULL`, id).
		Scan(&nidEnc, &birthEnc, &mobEnc, &state)
	if errors.Is(err, pgx.ErrNoRows) || state != "pending" {
		return nil
	}
	if err != nil {
		return err
	}
	nid, e1 := s.crypto.Open("pilgrim.nid", nidEnc)
	bs, e2 := s.crypto.Open("pilgrim.birth", birthEnc)
	mobile, e3 := s.crypto.Open("pilgrim.mobile", mobEnc)
	if e1 != nil || e2 != nil || e3 != nil {
		return errors.New("cannot decrypt pilgrim record")
	}
	birth, err := time.Parse("2006-01-02", bs)
	if err != nil {
		return err
	}
	_, err = s.verifyNow(ctx, id, nid, birth, mobile)
	return err
}

func (s *Service) activeTents(ctx context.Context, pilgrimID uuid.UUID) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT t.code FROM assignments a JOIN tents t ON t.id=a.tent_id
		WHERE a.pilgrim_id=$1 AND a.returned_at IS NULL AND a.cancelled_at IS NULL AND a.lost_at IS NULL ORDER BY t.code`, pilgrimID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetPilgrim returns a masked view; every call is audited.
func (s *Service) GetPilgrim(ctx context.Context, a Actor, id uuid.UUID) (*PilgrimView, error) {
	if err := a.require(domain.PermPilgrimRead); err != nil {
		return nil, err
	}
	var row pilgrimRow
	err := s.db.QueryRow(ctx, `SELECT id, nid_enc, mobile_enc, name_enc, verification::text FROM pilgrims WHERE id=$1 AND anonymized_at IS NULL`, id).
		Scan(&row.id, &row.nidEnc, &row.mobileEnc, &row.nameEnc, &row.verification)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, NotFound("زائر پیدا نشد")
	}
	if err != nil {
		return nil, err
	}
	return s.viewOf(ctx, a, row)
}

func (s *Service) viewOf(ctx context.Context, a Actor, row pilgrimRow) (*PilgrimView, error) {
	nid, _ := s.crypto.Open("pilgrim.nid", row.nidEnc)
	mobile, _ := s.crypto.Open("pilgrim.mobile", row.mobileEnc)
	v := &PilgrimView{ID: row.id, Verification: row.verification, NationalID: domain.MaskNationalID(nid),
		Mobile: domain.MaskMobile(mobile), Existing: true}
	if row.nameEnc != nil {
		v.FullName, _ = s.crypto.Open("pilgrim.name", row.nameEnc)
	}
	tents, err := s.activeTents(ctx, row.id)
	if err != nil {
		return nil, err
	}
	v.ActiveTents = tents
	s.finish(v, nid)
	s.Audit(a, "pilgrim.view", "pilgrim", row.id.String(), nil)
	return v, nil
}

// FindPilgrims looks pilgrims up by national ID or mobile through the blind
// indexes (nothing is decrypted to search).
func (s *Service) FindPilgrims(ctx context.Context, a Actor, nationalID, mobile string) ([]PilgrimView, error) {
	if err := a.require(domain.PermPilgrimRead); err != nil {
		return nil, err
	}
	var rows pgx.Rows
	var err error
	switch {
	case nationalID != "":
		nid := domain.NormalizeDigits(nationalID)
		rows, err = s.db.Query(ctx, `SELECT id, nid_enc, mobile_enc, name_enc, verification::text FROM pilgrims WHERE nid_idx=$1 AND anonymized_at IS NULL`,
			s.crypto.Index("nid", nid))
	case mobile != "":
		m, e := domain.NormalizeMobile(mobile)
		if e != nil {
			return nil, Unprocessable("bad_mobile", "شماره موبایل نامعتبر است")
		}
		rows, err = s.db.Query(ctx, `SELECT id, nid_enc, mobile_enc, name_enc, verification::text FROM pilgrims WHERE mobile_idx=$1 AND anonymized_at IS NULL LIMIT 5`,
			s.crypto.Index("mobile", m))
	default:
		return nil, BadRequest("missing_query", "کد ملی یا موبایل را وارد کنید")
	}
	if err != nil {
		return nil, err
	}
	var found []pilgrimRow
	for rows.Next() {
		var r pilgrimRow
		if err := rows.Scan(&r.id, &r.nidEnc, &r.mobileEnc, &r.nameEnc, &r.verification); err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]PilgrimView, 0, len(found))
	for _, r := range found {
		v, err := s.viewOf(ctx, a, r)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

// PilgrimMobile decrypts a mobile number for the SMS worker only.
func (s *Service) PilgrimMobile(ctx context.Context, id uuid.UUID) (string, error) {
	var enc []byte
	err := s.db.QueryRow(ctx, `SELECT mobile_enc FROM pilgrims WHERE id=$1 AND anonymized_at IS NULL`, id).Scan(&enc)
	if err != nil {
		return "", err
	}
	return s.crypto.Open("pilgrim.mobile", enc)
}

// AnonymizeInactive removes personal data of pilgrims that have no active
// tents and have not been seen for the retention period. The row stays so
// historical counts remain correct.
func (s *Service) AnonymizeInactive(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `UPDATE pilgrims p SET nid_enc=NULL, nid_idx=NULL, birth_enc=NULL, mobile_enc=NULL, mobile_idx=NULL,
		name_enc=NULL, anonymized_at=now()
		WHERE anonymized_at IS NULL AND last_seen_at < now() - make_interval(days => $1)
		AND NOT EXISTS (SELECT 1 FROM assignments a WHERE a.pilgrim_id=p.id AND a.returned_at IS NULL AND a.cancelled_at IS NULL AND a.lost_at IS NULL)`,
		s.cfg.RetentionDays)
	if err != nil {
		return 0, err
	}
	if n := tag.RowsAffected(); n > 0 {
		s.AuditNow(ctx, nil, "", "pilgrim.anonymize", "pilgrim", "", map[string]any{"count": n})
	}
	return tag.RowsAffected(), nil
}
