#!/usr/bin/env python3
"""Generate the print layout for the satin tent labels (QR + shrine name + tent ID).

Outputs (labels/output/):
  chador-labels-print.pdf     art for the printer: vector, text converted to outlines
  chador-labels-cutguide.pdf  cut contours only, same page size (for laser / hot-knife cutting)
  chador-labels-proof.pdf     print art + cut contour (red) + sewing-safe area (blue) for checking
  tent_ids.csv                every ID with its row/column on the sheet

Run:  python3 generate_labels.py [--tents 1000]
Needs: segno, reportlab, fonttools, uharfbuzz
"""
import argparse
import csv
from pathlib import Path

import segno
import uharfbuzz as hb
from fontTools.pens.basePen import BasePen
from fontTools.pens.boundsPen import BoundsPen
from fontTools.ttLib import TTFont
from reportlab.lib.colors import CMYKColor
from reportlab.lib.units import mm
from reportlab.pdfgen import canvas

HERE = Path(__file__).parent
FONT_PATH = HERE / "fonts" / "Vazirmatn-Bold.ttf"
OUT = HERE / "output"

# ---- geometry (all in mm) -------------------------------------------------
SHEET_W = 1000.0        # fabric width: 100 cm
LABEL_W = 32.0          # finished label size
LABEL_H = 40.0
GAP = 2.0               # waste strip between labels (cut through it)
SEAM = 4.0              # nothing printed within this distance of the cut edge (sewing allowance)
MARGIN_Y = 12.0         # top / bottom margin of the sheet (crop marks, slug line)
QR_SIZE = 20.0          # QR symbol size; module size follows from the QR version (v1 = 21 modules -> 0.95 mm)

HEADER_TEXT = "حرم مطهر شاهچراغ"
ID_PREFIX = "CH-"
ID_DIGITS = 6

# vertical plan inside the sewing-safe area (mm from its top edge)
HEADER_BAND = (0.0, 4.0)
QR_TOP = 6.0
ID_BASELINE = 31.0
ID_FONT_MM = 3.8

BLACK = CMYKColor(0, 0, 0, 1)    # K-only black: no registration colour fringes
RED = CMYKColor(0, 1, 1, 0)
BLUE = CMYKColor(1, 0.6, 0, 0)


class PathPen(BasePen):
    """Collects glyph outlines into a reportlab path, scaled and flipped to PDF space."""

    def __init__(self, glyph_set, path, ox, oy, scale):
        super().__init__(glyph_set)
        self.p, self.ox, self.oy, self.s = path, ox, oy, scale

    def _pt(self, pt):
        return (self.ox + pt[0] * self.s, self.oy + pt[1] * self.s)

    def _moveTo(self, pt):
        self.p.moveTo(*self._pt(pt))

    def _lineTo(self, pt):
        self.p.lineTo(*self._pt(pt))

    def _curveToOne(self, a, b, c):
        self.p.curveTo(*self._pt(a), *self._pt(b), *self._pt(c))

    def _closePath(self):
        self.p.close()


class Shaper:
    def __init__(self, font_path):
        self.tt = TTFont(str(font_path))
        self.gs = self.tt.getGlyphSet()
        self.order = self.tt.getGlyphOrder()
        self.upem = self.tt["head"].unitsPerEm
        blob = hb.Blob.from_file_path(str(font_path))
        self.hb_font = hb.Font(hb.Face(blob))

    def shape(self, text, rtl):
        buf = hb.Buffer()
        buf.add_str(text)
        buf.guess_segment_properties()
        buf.direction = "rtl" if rtl else "ltr"
        hb.shape(self.hb_font, buf, {"kern": True, "liga": True})
        x, glyphs = 0, []
        for info, pos in zip(buf.glyph_infos, buf.glyph_positions):
            glyphs.append((self.order[info.codepoint], x + pos.x_offset, pos.y_offset))
            x += pos.x_advance
        return glyphs, x

    def bounds(self, glyphs):
        xs0, ys0, xs1, ys1 = [], [], [], []
        for name, gx, gy in glyphs:
            bp = BoundsPen(self.gs)
            self.gs[name].draw(bp)
            if bp.bounds:
                a, b, c, d = bp.bounds
                xs0.append(a + gx); ys0.append(b + gy); xs1.append(c + gx); ys1.append(d + gy)
        return min(xs0), min(ys0), max(xs1), max(ys1)

    def add_to_path(self, path, glyphs, ox, oy, scale):
        for name, gx, gy in glyphs:
            self.gs[name].draw(PathPen(self.gs, path, ox + gx * scale, oy + gy * scale, scale))


def qr_runs(payload):
    """Dark modules as horizontal runs (row, col_start, length); error correction level H."""
    qr = segno.make(payload, error="h", micro=False, boost_error=False)
    assert qr.version == 1, f"unexpected QR version {qr.version} for {payload}"
    rows = [list(r) for r in qr.matrix]
    runs = []
    for r, row in enumerate(rows):
        c = 0
        while c < len(row):
            if row[c]:
                start = c
                while c < len(row) and row[c]:
                    c += 1
                runs.append((r, start, c - start))
            else:
                c += 1
    return runs, len(rows)


def grid(total_ids):
    cols = int((SHEET_W + GAP - 2 * 8.0) // (LABEL_W + GAP))
    rows = -(-total_ids // cols)
    width_used = cols * LABEL_W + (cols - 1) * GAP
    margin_x = (SHEET_W - width_used) / 2
    sheet_h = 2 * MARGIN_Y + rows * LABEL_H + (rows - 1) * GAP
    return cols, rows, margin_x, sheet_h


def build(tents):
    cols0 = int((SHEET_W + GAP - 2 * 8.0) // (LABEL_W + GAP))
    rows = -(-tents // cols0)
    cols, rows, margin_x, sheet_h = grid(cols0 * rows)
    total = cols * rows                   # fill the whole grid; extras are spares
    OUT.mkdir(exist_ok=True)

    shaper = Shaper(FONT_PATH)
    sf = shaper.upem

    # header text: fit into the band, same on every label
    hg, hadv = shaper.shape(HEADER_TEXT, rtl=True)
    hx0, hy0, hx1, hy1 = shaper.bounds(hg)
    inner_w = LABEL_W - 2 * SEAM
    band_h = HEADER_BAND[1] - HEADER_BAND[0]
    h_scale = min(inner_w / (hx1 - hx0), band_h / (hy1 - hy0))     # mm per font unit

    id_scale = ID_FONT_MM / sf

    def label_origin(r, c):
        """Top-left of a label in mm, y measured from the top of the sheet."""
        return margin_x + c * (LABEL_W + GAP), MARGIN_Y + r * (LABEL_H + GAP)

    def to_pdf(x_mm, y_from_top_mm):
        return x_mm * mm, (sheet_h - y_from_top_mm) * mm

    def draw_art(cv):
        cv.setFillColor(BLACK)
        # header outlines once, reused as a form on every label
        cv.beginForm("header")
        p = cv.beginPath()
        hw, hh = (hx1 - hx0) * h_scale, (hy1 - hy0) * h_scale
        ox = (inner_w - hw) / 2 - hx0 * h_scale
        oy = (band_h - hh) / 2 - hy0 * h_scale
        shaper.add_to_path(p, hg, ox * mm, oy * mm, h_scale * mm)
        cv.drawPath(p, stroke=0, fill=1)
        cv.endForm()

        ids = []
        for i in range(total):
            r, c = divmod(i, cols)
            tid = f"{ID_PREFIX}{i + 1:0{ID_DIGITS}d}"
            ids.append((tid, r + 1, c + 1))
            lx, ly = label_origin(r, c)
            ix, iy = lx + SEAM, ly + SEAM             # sewing-safe area, top-left

            # header
            hx, hy = to_pdf(ix, iy + band_h)          # bottom-left of the band
            cv.saveState(); cv.translate(hx, hy); cv.doForm("header"); cv.restoreState()

            # QR
            runs, n = qr_runs(tid)
            module = QR_SIZE / n
            sym = QR_SIZE
            qx = ix + (inner_w - sym) / 2
            qy = iy + QR_TOP
            p = cv.beginPath()
            for rr, cs, ln in runs:
                x0, y0 = to_pdf(qx + cs * module, qy + (rr + 1) * module)
                p.rect(x0, y0, ln * module * mm, module * mm)
            cv.drawPath(p, stroke=0, fill=1)

            # ID text (Latin, left-to-right)
            g, adv = shaper.shape(tid, rtl=False)
            tx = ix + (inner_w - adv * id_scale) / 2
            bx, by = to_pdf(tx, iy + ID_BASELINE)
            p = cv.beginPath()
            shaper.add_to_path(p, g, bx, by, id_scale * mm)
            cv.drawPath(p, stroke=0, fill=1)
        return ids

    def draw_marks(cv):
        """Crop marks at the four corners of the label grid + a slug line in the margin."""
        cv.setStrokeColor(BLACK); cv.setLineWidth(0.25)
        gx0, gy0 = margin_x, MARGIN_Y
        gx1 = margin_x + cols * LABEL_W + (cols - 1) * GAP
        gy1 = MARGIN_Y + rows * LABEL_H + (rows - 1) * GAP
        L = 5.0
        for x, sx in ((gx0, -1), (gx1, 1)):
            for y, sy in ((gy0, -1), (gy1, 1)):
                a = to_pdf(x + sx * 1.5, y); b = to_pdf(x + sx * (1.5 + L), y)
                cv.line(*a, *b)
                a = to_pdf(x, y + sy * 1.5); b = to_pdf(x, y + sy * (1.5 + L))
                cv.line(*a, *b)
        slug = (f"SHAHCHERAGH CHADOR LABELS  {ID_PREFIX}{1:0{ID_DIGITS}d}-{ID_PREFIX}{total:0{ID_DIGITS}d}  "
                f"LABEL {LABEL_W:g}x{LABEL_H:g} MM  SEAM {SEAM:g} MM  PRINT AT 100% - DO NOT SCALE")
        g, adv = shaper.shape(slug, rtl=False)
        p = cv.beginPath()
        x, y = to_pdf(margin_x, 6.0)
        shaper.add_to_path(p, g, x, y, (2.2 / sf) * mm)
        cv.setFillColor(BLACK)
        cv.drawPath(p, stroke=0, fill=1)

    def draw_cut(cv, colour, dash=None, inset=0.0, width=0.25):
        cv.setStrokeColor(colour); cv.setLineWidth(width)
        cv.setDash(*dash) if dash else cv.setDash()
        for i in range(total):
            r, c = divmod(i, cols)
            lx, ly = label_origin(r, c)
            x, y = to_pdf(lx + inset, ly + LABEL_H - inset)
            cv.rect(x, y, (LABEL_W - 2 * inset) * mm, (LABEL_H - 2 * inset) * mm, stroke=1, fill=0)

    def page(name, *steps):
        cv = canvas.Canvas(str(OUT / name), pagesize=(SHEET_W * mm, sheet_h * mm), pageCompression=1)
        cv.setTitle(name)
        result = None
        for s in steps:
            r = s(cv)
            result = r if r is not None else result
        cv.showPage(); cv.save()
        return result

    ids = page("chador-labels-print.pdf", draw_art, draw_marks)
    page("chador-labels-cutguide.pdf", draw_marks, lambda cv: draw_cut(cv, BLACK))
    page("chador-labels-proof.pdf", draw_art, draw_marks,
         lambda cv: draw_cut(cv, RED),
         lambda cv: draw_cut(cv, BLUE, dash=(1.5, 1.5), inset=SEAM, width=0.2))

    with open(OUT / "tent_ids.csv", "w", newline="", encoding="utf-8") as f:
        w = csv.writer(f)
        w.writerow(["tent_id", "sheet_row", "sheet_col", "kind"])
        for tid, r, c in ids:
            w.writerow([tid, r, c, "tent" if int(tid[len(ID_PREFIX):]) <= tents else "spare"])

    print(f"sheet {SHEET_W / 10:g} x {sheet_h / 10:g} cm | grid {cols} x {rows} = {total} labels "
          f"({tents} tents + {total - tents} spares) | header font scale {h_scale * sf:.2f} mm")
    return dict(cols=cols, rows=rows, total=total, sheet_h=sheet_h, margin_x=margin_x)


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--tents", type=int, default=1000)
    build(ap.parse_args().tents)
