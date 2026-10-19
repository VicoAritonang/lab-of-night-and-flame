"""Bundled synthetic datasets (FR-001). Generated deterministically from fixed seeds, so every
fresh install produces byte-identical snapshots (same digests).

All of these are SYNTHETIC and labelled as such; they demonstrate the Lab, they are not
external benchmarks.
"""
from __future__ import annotations

import io
import math
import random
from typing import Any

from PIL import Image, ImageDraw, ImageFilter, ImageFont

from ..db import Database
from .store import ArtifactStore, SampleIn, create_dataset

# ------------------------------------------------------------------ text datasets
VENDORS = ["Acme Supplies", "Nusantara Logistik", "Blue Harbor Ltd", "Kopi Kenangan Group", "Vertex Labs",
           "PT Sinar Jaya", "Northwind Traders", "Orion Medical", "Delta Fabrication", "Cendana Studio"]
CURRENCIES = [("USD", "$"), ("EUR", "€"), ("IDR", "Rp")]
MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September",
          "October", "November", "December"]

INVOICE_SCHEMA = {
    "type": "object",
    "required": ["invoice_number", "vendor", "date", "currency", "total"],
    "properties": {
        "invoice_number": {"type": "string"},
        "vendor": {"type": "string"},
        "date": {"type": "string", "pattern": r"^\d{4}-\d{2}-\d{2}$"},
        "currency": {"type": "string", "enum": ["USD", "EUR", "IDR"]},
        "total": {"type": "number"},
    },
}


def _invoices(rng: random.Random, n: int = 36) -> list[SampleIn]:
    out = []
    for i in range(n):
        family = ("formal", "email", "receipt")[i % 3]
        vendor = rng.choice(VENDORS)
        cur, sym = rng.choice(CURRENCIES)
        y, m, d = rng.choice([2025, 2026]), rng.randint(1, 12), rng.randint(1, 28)
        qty_lines = [(rng.choice(["Widget", "Consulting hour", "Cable", "License", "Shipping", "Sensor"]),
                      rng.randint(1, 9), round(rng.uniform(5, 400) * (1000 if cur == "IDR" else 1), 2)) for _ in range(rng.randint(2, 4))]
        subtotal = round(sum(q * p for _, q, p in qty_lines), 2)
        tax = round(subtotal * 0.11, 2)
        total = round(subtotal + tax, 2)
        inv = f"INV-{y}-{rng.randint(1000, 9999)}"
        money = (lambda v: f"{sym} {v:,.2f}") if cur != "IDR" else (lambda v: f"Rp {v:,.0f}".replace(",", "."))
        lines = "\n".join(f"  {name} x{q} @ {money(p)} = {money(q * p)}" for name, q, p in qty_lines)
        if family == "formal":
            text = (f"INVOICE\nNumber: {inv}\nIssued by: {vendor}\nDate: {y}-{m:02d}-{d:02d}\n\nItems:\n{lines}\n"
                    f"Subtotal: {money(subtotal)}\nVAT 11%: {money(tax)}\nTOTAL DUE: {money(total)}\nCurrency: {cur}")
        elif family == "email":
            text = (f"Hi team,\n\nPlease find our bill below. Reference {inv}, dated {MONTHS[m - 1]} {d}, {y}.\n{lines}\n"
                    f"Before tax that comes to {money(subtotal)}; with 11% VAT ({money(tax)}) the amount payable is "
                    f"{money(total)}.\n\nRegards,\nAccounts — {vendor}")
        else:
            text = (f"*** {vendor.upper()} ***\nreceipt#{inv}\n{d:02d} {MONTHS[m - 1][:3]} {y}\n{lines}\n-----\n"
                    f"sub {money(subtotal)}\ntax {money(tax)}\nTOTAL {money(total)} ({cur})")
        out.append(SampleIn(
            key=f"inv-{i:03d}", text=text, split="test" if i % 6 else "dev", group=family,
            expected={"invoice_number": inv, "vendor": vendor, "date": f"{y}-{m:02d}-{d:02d}", "currency": cur, "total": total},
            meta={"template_family": family, "currency": cur, "line_items": len(qty_lines)},
        ))
    return out


def _arithmetic(rng: random.Random, n: int = 40) -> list[SampleIn]:
    out = []
    names = ["Ayu", "Budi", "Chen", "Dewi", "Eko", "Farah", "Gita", "Hana", "Ivan", "Joko"]
    for i in range(n):
        kind = ("shopping", "rate", "percent", "multi_step")[i % 4]
        nm = rng.choice(names)
        if kind == "shopping":
            a, pa, b, pb = rng.randint(2, 9), rng.randint(3, 25), rng.randint(2, 9), rng.randint(3, 25)
            text = f"{nm} buys {a} notebooks at ${pa} each and {b} pens at ${pb} each. How many dollars does {nm} spend in total?"
            ans = a * pa + b * pb
            difficulty = "easy"
        elif kind == "rate":
            speed, hours, extra = rng.randint(30, 90), rng.randint(2, 6), rng.randint(5, 40)
            text = (f"A train travels at {speed} km/h for {hours} hours, then another {extra} km at the end. "
                    f"What is the total distance in km?")
            ans = speed * hours + extra
            difficulty = "easy"
        elif kind == "percent":
            price, pct = rng.randint(4, 40) * 25, rng.choice([10, 15, 20, 25, 30, 40])
            text = f"A jacket costs ${price}. It is discounted by {pct}%. What is the sale price in dollars?"
            ans = price * (100 - pct) / 100
            difficulty = "medium"
        else:
            boxes, per, broken, given = rng.randint(3, 12), rng.randint(6, 24), rng.randint(1, 9), rng.randint(2, 15)
            text = (f"{nm} has {boxes} boxes with {per} eggs each. {broken} eggs break, and {nm} gives away "
                    f"{given} eggs. How many eggs are left?")
            ans = boxes * per - broken - given
            difficulty = "medium"
        out.append(SampleIn(key=f"math-{i:03d}", text=text, expected=ans, split="test", group=kind,
                            meta={"problem_type": kind, "difficulty": difficulty}))
    return out


MCQ = [
    ("What is the chemical symbol for gold?", ["Ag", "Au", "Gd", "Go"], "B", "science"),
    ("Which planet is closest to the Sun?", ["Venus", "Earth", "Mercury", "Mars"], "C", "science"),
    ("What is the boiling point of water at sea level in Celsius?", ["90", "100", "110", "120"], "B", "science"),
    ("How many sides does a hexagon have?", ["5", "6", "7", "8"], "B", "math"),
    ("What is the square root of 144?", ["10", "11", "12", "14"], "C", "math"),
    ("What is 15% of 200?", ["15", "20", "30", "45"], "C", "math"),
    ("Which data structure uses FIFO ordering?", ["Stack", "Queue", "Binary tree", "Hash map"], "B", "computing"),
    ("What is the average-case time complexity of binary search?", ["O(n)", "O(log n)", "O(n log n)", "O(1)"], "B", "computing"),
    ("Which HTTP status code means 'Not Found'?", ["200", "301", "404", "500"], "C", "computing"),
    ("In Python, which keyword defines a function?", ["func", "def", "lambda", "function"], "B", "computing"),
    ("Which protocol is used to securely browse websites?", ["FTP", "HTTP", "HTTPS", "SMTP"], "C", "computing"),
    ("What is the capital city of Indonesia (as of 2024 government seat)?", ["Surabaya", "Bandung", "Jakarta", "Medan"], "C", "geography"),
    ("Which is the largest ocean on Earth?", ["Atlantic", "Indian", "Arctic", "Pacific"], "D", "geography"),
    ("On which continent is Kenya located?", ["Asia", "Africa", "South America", "Europe"], "B", "geography"),
    ("Which river flows through Cairo?", ["Nile", "Amazon", "Danube", "Mekong"], "A", "geography"),
    ("Who wrote 'Romeo and Juliet'?", ["Charles Dickens", "William Shakespeare", "Jane Austen", "Mark Twain"], "B", "literature"),
    ("What gas do plants primarily absorb for photosynthesis?", ["Oxygen", "Nitrogen", "Carbon dioxide", "Helium"], "C", "science"),
    ("How many bits are in one byte?", ["4", "8", "16", "32"], "B", "computing"),
    ("What is the largest planet in our solar system?", ["Saturn", "Jupiter", "Neptune", "Earth"], "B", "science"),
    ("Which number is prime?", ["21", "27", "29", "33"], "C", "math"),
    ("What does CPU stand for?", ["Central Processing Unit", "Computer Personal Unit", "Core Power Utility", "Central Program Utility"], "A", "computing"),
    ("Which language is primarily spoken in Brazil?", ["Spanish", "Portuguese", "French", "English"], "B", "geography"),
    ("Apa ibu kota provinsi Jawa Barat?", ["Semarang", "Bandung", "Surabaya", "Yogyakarta"], "B", "geography"),
    ("Berapa hasil dari 7 x 8?", ["54", "56", "58", "64"], "B", "math"),
]

SENTIMENT = [
    ("The battery lasts all day and the screen is gorgeous.", "positive", "en"),
    ("Arrived broken and support never answered my emails.", "negative", "en"),
    ("It works. Nothing special, nothing terrible.", "neutral", "en"),
    ("Absolutely love it, best purchase this year!", "positive", "en"),
    ("The app crashes every time I open the camera.", "negative", "en"),
    ("Delivery took five days, which was the estimate.", "neutral", "en"),
    ("Setup was painless and the docs are excellent.", "positive", "en"),
    ("Overpriced for what you get. Would not recommend.", "negative", "en"),
    ("The color is slightly different from the photo.", "neutral", "en"),
    ("Customer service went above and beyond to help me.", "positive", "en"),
    ("Stopped charging after two weeks.", "negative", "en"),
    ("I received the item described in the listing.", "neutral", "en"),
    ("Not bad at all — I expected worse, honestly it's great.", "positive", "en"),
    ("Great, another update that deletes my settings. Fantastic.", "negative", "en"),
    ("Pengirimannya cepat dan barangnya sesuai, mantap!", "positive", "id"),
    ("Kualitasnya jelek, baru dipakai sehari sudah rusak.", "negative", "id"),
    ("Barang sudah diterima, belum dicoba.", "neutral", "id"),
    ("Pelayanannya ramah sekali, saya puas.", "positive", "id"),
    ("Kecewa berat, tidak sesuai deskripsi.", "negative", "id"),
    ("Ukurannya standar seperti biasa.", "neutral", "id"),
    ("Rasanya enak banget, pasti beli lagi.", "positive", "id"),
    ("Aplikasinya sering error dan lambat.", "negative", "id"),
    ("Paket datang hari Selasa.", "neutral", "id"),
    ("Harganya terjangkau dan kualitasnya bagus.", "positive", "id"),
]


# ------------------------------------------------------------------ image datasets
PALETTE = {"red": (220, 50, 47), "blue": (38, 110, 220), "green": (40, 160, 70), "orange": (240, 140, 20), "purple": (130, 70, 190)}


def _png(im: Image.Image) -> bytes:
    buf = io.BytesIO()
    im.save(buf, format="PNG", optimize=True)
    return buf.getvalue()


def _font(size: int) -> ImageFont.FreeTypeFont | ImageFont.ImageFont:
    try:
        return ImageFont.load_default(size=size)
    except TypeError:  # very old Pillow
        return ImageFont.load_default()


def _overlaps(box, boxes, pad=6):
    x1, y1, x2, y2 = box
    return any(not (x2 + pad < a or x1 - pad > c or y2 + pad < b or y1 - pad > d) for a, b, c, d in boxes)


def _shapes_image(rng: random.Random, target_color: str, target_shape: str, n_target: int, n_distract: int):
    w, h = 384, 288
    im = Image.new("RGB", (w, h), (248, 248, 246))
    dr = ImageDraw.Draw(im)
    placed: list[tuple[int, int, int, int]] = []

    def place(color: str, shape: str):
        for _ in range(400):
            s = rng.randint(22, 40)
            x, y = rng.randint(8, w - s - 8), rng.randint(8, h - s - 8)
            box = (x, y, x + s, y + s)
            if not _overlaps(box, placed):
                placed.append(box)
                (dr.ellipse if shape == "circle" else dr.rectangle)(box, fill=PALETTE[color])
                return True
        return False

    placed_targets = sum(place(target_color, target_shape) for _ in range(n_target))
    for _ in range(n_distract):
        c = rng.choice(list(PALETTE))
        sh = rng.choice(["circle", "square"])
        if c == target_color and sh == target_shape:
            sh = "square" if target_shape == "circle" else "circle"
        place(c, sh)
    return im, placed_targets


def _bar_chart(rng: random.Random, labels: list[str], values: list[int]) -> Image.Image:
    w, h = 420, 300
    im = Image.new("RGB", (w, h), "white")
    dr = ImageDraw.Draw(im)
    f, fs = _font(14), _font(12)
    left, bottom, top = 50, h - 40, 30
    vmax = 100
    dr.line((left, top, left, bottom), fill=(90, 90, 90), width=1)
    dr.line((left, bottom, w - 20, bottom), fill=(90, 90, 90), width=1)
    for t in range(0, vmax + 1, 20):
        y = bottom - (bottom - top) * t / vmax
        dr.line((left - 4, y, w - 20, y), fill=(225, 225, 225) if t else (90, 90, 90))
        dr.text((left - 34, y - 7), f"{t:>3}", fill=(80, 80, 80), font=fs)
    slot = (w - 20 - left) / len(labels)
    for i, (lab, v) in enumerate(zip(labels, values)):
        x1 = left + i * slot + slot * 0.2
        x2 = left + (i + 1) * slot - slot * 0.2
        y = bottom - (bottom - top) * v / vmax
        dr.rectangle((x1, y, x2, bottom), fill=(42, 120, 214))
        dr.text((x1 + (x2 - x1) / 2 - 8, y - 18), str(v), fill=(20, 20, 20), font=fs)
        dr.text((x1 + (x2 - x1) / 2 - 5, bottom + 8), lab, fill=(20, 20, 20), font=f)
    dr.text((left, 6), "Units sold per region", fill=(20, 20, 20), font=f)
    return im


def _ocr_image(rng: random.Random, code: str, noise: str) -> Image.Image:
    w, h = 360, 120
    im = Image.new("RGB", (w, h), (250, 250, 250))
    dr = ImageDraw.Draw(im)
    f = _font(40)
    dr.text((30, 36), code, fill=(25, 25, 25), font=f)
    if noise in ("medium", "high"):
        for _ in range(60 if noise == "medium" else 160):
            x, y = rng.randint(0, w), rng.randint(0, h)
            dr.point((x, y), fill=(rng.randint(80, 200),) * 3)
        im = im.rotate(rng.uniform(-4, 4) if noise == "medium" else rng.uniform(-9, 9), fillcolor=(250, 250, 250))
    if noise == "high":
        im = im.filter(ImageFilter.GaussianBlur(1.1))
    return im


async def _vision_samples(art: ArtifactStore) -> dict[str, list[SampleIn]]:
    rng = random.Random(4242)
    counting: list[SampleIn] = []
    for i in range(20):
        color, shape = rng.choice(list(PALETTE)), rng.choice(["circle", "square"])
        n = rng.randint(1, 7)
        im, placed = _shapes_image(rng, color, shape, n, rng.randint(3, 9))
        d = (await art.put_image(_png(im)))["digest"]
        counting.append(SampleIn(key=f"count-{i:03d}", text=f"How many {color} {shape}s are in the image? Answer with a single number.",
                                 images=[d], expected=placed, meta={"target_color": color, "target_shape": shape,
                                                                   "count_bucket": "1-3" if placed <= 3 else "4-7"}))
    charts: list[SampleIn] = []
    for i in range(15):
        labels = ["A", "B", "C", "D", "E"][: rng.randint(3, 5)]
        values = [rng.randint(1, 19) * 5 for _ in labels]
        im = _bar_chart(rng, labels, values)
        d = (await art.put_image(_png(im)))["digest"]
        q = rng.choice(["value", "max", "diff"])
        if q == "value":
            j = rng.randrange(len(labels))
            text, ans = f"What is the value of bar {labels[j]}?", values[j]
        elif q == "max":
            text, ans = "What is the value of the tallest bar?", max(values)
        else:
            text, ans = f"What is the value of bar {labels[0]} minus bar {labels[-1]}?", values[0] - values[-1]
        charts.append(SampleIn(key=f"chart-{i:03d}", text=text + " Answer with a single number.", images=[d], expected=ans,
                               meta={"question_type": q, "bars": len(labels)}))
    ocr: list[SampleIn] = []
    alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
    for i in range(18):
        code = "".join(rng.choice(alphabet) for _ in range(4)) + "-" + "".join(rng.choice(alphabet) for _ in range(4))
        noise = ("clean", "medium", "high")[i % 3]
        d = (await art.put_image(_png(_ocr_image(rng, code, noise))))["digest"]
        ocr.append(SampleIn(key=f"ocr-{i:03d}", text="Transcribe the code shown in the image exactly. Reply with the code only.",
                            images=[d], expected=code, group=f"code-{i}", meta={"noise": noise, "perturbation": "rotation+speckle+blur" if noise == "high" else ("rotation+speckle" if noise == "medium" else "none")}))
    grounding: list[SampleIn] = []
    for i in range(12):
        w, h = 400, 300
        im = Image.new("RGB", (w, h), (246, 246, 244))
        dr = ImageDraw.Draw(im)
        boxes = []
        target = rng.choice(list(PALETTE))
        others = [c for c in PALETTE if c != target]
        target_box = None
        for c in [target] + rng.sample(others, 2):
            for _ in range(200):
                bw, bh = rng.randint(40, 110), rng.randint(40, 100)
                x, y = rng.randint(5, w - bw - 5), rng.randint(5, h - bh - 5)
                box = (x, y, x + bw, y + bh)
                if not _overlaps(box, boxes, 10):
                    boxes.append(box)
                    dr.rectangle(box, fill=PALETTE[c])
                    if c == target:
                        target_box = list(box)
                    break
        d = (await art.put_image(_png(im)))["digest"]
        grounding.append(SampleIn(
            key=f"ground-{i:03d}",
            text=(f"The image is {w}x{h} pixels. Return the bounding box of the {target} rectangle as JSON "
                  f'{{"bbox": [x1, y1, x2, y2]}} in pixel coordinates (origin top-left).'),
            images=[d], expected={"bbox": target_box}, meta={"target_color": target, "box_area": (target_box[2] - target_box[0]) * (target_box[3] - target_box[1]) if target_box else None}))
    return {"counting": counting, "charts": charts, "ocr": ocr, "grounding": grounding}


SYNTH_CARD = {
    "origin": "Agent Reliability Lab built-in generator", "collection_method": "procedural synthetic generation (fixed seed)",
    "license": "CC0 (generated)", "sensitive_fields": [], "classification": "public",
    "label_source": "programmatic ground truth from the generator", "contamination": "not derived from public benchmarks",
}


def _hard_reasoning(rng: random.Random, n: int) -> list[tuple[str, float, str]]:
    """Multi-step problems with programmatically exact answers (question, answer, kind)."""
    out = []
    for i in range(n):
        kind = ("inclusion_exclusion", "compound", "recurrence", "work_rate")[i % 4]
        if kind == "inclusion_exclusion":
            N, a, b = rng.randint(200, 900), rng.choice([3, 4, 6, 7]), rng.choice([5, 9, 11, 13])
            lcm = a * b // math.gcd(a, b)
            ans = N // a + N // b - N // lcm
            q = f"How many integers from 1 to {N} inclusive are divisible by {a} or by {b}?"
        elif kind == "compound":
            P, r, yrs = rng.choice([1000, 2500, 4000, 8000]), rng.choice([3, 4, 5, 6, 8]), rng.randint(3, 6)
            ans = round(P * (1 + r / 100) ** yrs)
            q = f"${P} is invested at {r}% interest compounded annually. What is the balance after {yrs} years, rounded to the nearest dollar?"
        elif kind == "recurrence":
            a0, k = rng.randint(3, 9), rng.randint(1, 4)
            t = a0
            for _ in range(6):
                t = 2 * t - k
            ans = t
            q = f"A sequence starts at {a0}. Each next term is twice the previous term minus {k}. What is the 7th term?"
        else:
            x, y = rng.choice([(2, 3), (3, 6), (4, 12), (6, 12), (10, 15), (5, 20)])
            ans = x * y / (x + y) * 60
            q = f"Pipe A fills a tank in {x} hours and pipe B fills it in {y} hours. Working together, how many minutes do they take?"
        out.append((q, float(ans), kind))
    return out


async def _router_mix(art: ArtifactStore) -> list[SampleIn]:
    """Mixed-difficulty, mixed-modality suite for evaluating model routers (e.g. typesafe/jev-router).

    Every sample carries its own instructions and its own scorer (meta.scorer) so a single
    campaign can measure whether a router sends easy work to cheap models and hard work to strong ones.
    """
    rng = random.Random(9001)
    out: list[SampleIn] = []
    nl = chr(10)
    for s in _arithmetic(random.Random(77), 8):
        out.append(SampleIn(key=f"rm-easy-{len(out):02d}", text=s.text + nl + "End with 'Answer: <number>'.", expected=s.expected,
                            meta={"task_family": "arithmetic", "difficulty": "easy", "scorer": "numeric", "scorer_params": {"abs_tol": 0.01}}))
    for q, a, kind in _hard_reasoning(rng, 8):
        out.append(SampleIn(key=f"rm-hard-{len(out):02d}", text=q + nl + "Think carefully. End with 'Answer: <number>'.", expected=a,
                            meta={"task_family": "reasoning", "difficulty": "hard", "problem": kind, "scorer": "numeric", "scorer_params": {"abs_tol": 0.5}}))
    for q, opts, ans, _subj in MCQ[:6]:
        options = nl.join(f"{'ABCD'[j]}. {o}" for j, o in enumerate(opts))
        out.append(SampleIn(key=f"rm-mcq-{len(out):02d}", text=q + nl + options + nl + "Reply with only the letter.",
                            expected=ans, meta={"task_family": "knowledge", "difficulty": "easy", "scorer": "choice"}))
    for s in _invoices(random.Random(55), 6):
        out.append(SampleIn(key=f"rm-ext-{len(out):02d}",
                            text="Extract invoice_number, vendor, date (YYYY-MM-DD), currency (USD|EUR|IDR) and total (number) as a JSON object only." + nl + nl + s.text,
                            expected=s.expected, meta={"task_family": "extraction", "difficulty": "medium", "scorer": "json_fields", "scorer_params": {"numeric_tolerance": 0.01}}))
    for t, lab, lang in SENTIMENT[::4]:
        out.append(SampleIn(key=f"rm-sent-{len(out):02d}", text="Classify the sentiment as positive, negative or neutral. Reply with one word." + nl + nl + t,
                            expected=lab, meta={"task_family": "classification", "difficulty": "easy", "language": lang, "scorer": "normalized_match"}))
    crng = random.Random(4243)
    for _ in range(6):
        labels = ["A", "B", "C", "D", "E"][: crng.randint(3, 5)]
        values = [crng.randint(1, 19) * 5 for _ in labels]
        d = (await art.put_image(_png(_bar_chart(crng, labels, values))))["digest"]
        j = crng.randrange(len(labels))
        out.append(SampleIn(key=f"rm-vis-{len(out):02d}", text=f"What is the value of bar {labels[j]} in the chart? Answer with a single number.",
                            images=[d], expected=values[j], meta={"task_family": "vision_chart", "difficulty": "medium", "scorer": "numeric", "scorer_params": {"abs_tol": 0}}))
    return out


async def ensure_builtin(db: Database, art: ArtifactStore) -> None:
    """Create any missing built-in dataset (idempotent per name, so upgrades add new suites)."""
    existing = {r["name"] for r in await db.fetchall("SELECT DISTINCT name FROM datasets WHERE builtin=1")}
    if "router-mix" not in existing:
        await create_dataset(db, name="router-mix", samples=await _router_mix(art), task_type="mixed",
                             description="40 mixed tasks (easy arithmetic, hard multi-step reasoning, knowledge MCQ, extraction, "
                                         "sentiment, chart vision). Built to test whether a model router picks the right model per task.",
                             instructions="Follow the task instructions exactly.",
                             card={**SYNTH_CARD, "recommended_scorers": [{"key": "auto"}],
                                   "slices": ["task_family", "difficulty"]}, builtin=True, synthetic=True)
    if existing - {"router-mix"}:
        return
    rng = random.Random(1337)
    specs: list[dict[str, Any]] = [
        dict(name="invoice-extraction", samples=_invoices(rng), task_type="extraction",
             description="Structured extraction of invoice fields from three template families (formal, email, receipt).",
             instructions=("Extract the invoice fields and reply with ONLY a JSON object with keys: invoice_number (string), "
                           "vendor (string), date (YYYY-MM-DD), currency (USD|EUR|IDR), total (number, including tax)."),
             card={"recommended_scorers": [{"key": "json_fields", "params": {"numeric_tolerance": 0.01}},
                                           {"key": "json_schema", "params": {"schema": INVOICE_SCHEMA}}],
                   "slices": ["template_family", "currency"]}),
        dict(name="arithmetic-word-problems", samples=_arithmetic(rng), task_type="reasoning",
             description="Grade-school arithmetic word problems with exact numeric answers.",
             instructions="Solve the problem. End your reply with 'Answer: <number>'.",
             card={"recommended_scorers": [{"key": "numeric", "params": {"abs_tol": 0.01}}], "slices": ["problem_type", "difficulty"]}),
        dict(name="general-knowledge-mcq", task_type="multiple_choice",
             samples=[SampleIn(key=f"mcq-{i:03d}", text=q + "\n" + "\n".join(f"{'ABCD'[j]}. {o}" for j, o in enumerate(opts)),
                               expected=ans, meta={"subject": subj, "language": "id" if q.startswith(("Apa", "Berapa")) else "en"})
                      for i, (q, opts, ans, subj) in enumerate(MCQ)],
             description="Stable-fact multiple-choice questions across science, math, computing, geography (EN + ID).",
             instructions="Answer the multiple-choice question. Reply with only the letter of the correct option.",
             card={"recommended_scorers": [{"key": "choice"}], "slices": ["subject", "language"]}),
        dict(name="sentiment-multilingual", task_type="classification",
             samples=[SampleIn(key=f"sent-{i:03d}", text=t, expected=lab, meta={"language": lang})
                      for i, (t, lab, lang) in enumerate(SENTIMENT)],
             description="3-way sentiment classification of short reviews in English and Bahasa Indonesia (incl. sarcasm).",
             instructions="Classify the sentiment of the review as positive, negative, or neutral. Reply with one word.",
             card={"recommended_scorers": [{"key": "normalized_match"}], "slices": ["language"]}),
    ]
    vision = await _vision_samples(art)
    specs += [
        dict(name="vision-shape-counting", samples=vision["counting"], task_type="vision_counting",
             description="Count colored shapes among distractors in generated images (384x288).",
             instructions="You are given an image. Answer the question precisely.",
             card={"recommended_scorers": [{"key": "numeric", "params": {"abs_tol": 0}}], "slices": ["count_bucket", "target_shape"]}),
        dict(name="vision-chart-reading", samples=vision["charts"], task_type="vision_chart",
             description="Read values from generated bar charts (value lookup, max, difference).",
             instructions="You are given a chart image. Answer the question with a number.",
             card={"recommended_scorers": [{"key": "numeric", "params": {"abs_tol": 0}}], "slices": ["question_type"]}),
        dict(name="vision-ocr-robustness", samples=vision["ocr"], task_type="vision_ocr",
             description="Transcribe 9-char codes under three perturbation intensities (clean / medium / high).",
             instructions="You are given an image containing a code. Transcribe it exactly.",
             card={"recommended_scorers": [{"key": "exact_match"}, {"key": "similarity", "params": {"threshold": 0.85}}],
                   "slices": ["noise"]}),
        dict(name="vision-grounding", samples=vision["grounding"], task_type="vision_grounding",
             description="Localize a colored rectangle; scored by IoU in original pixel coordinates.",
             instructions="You are given an image. Reply with only the requested JSON.",
             card={"recommended_scorers": [{"key": "bbox_iou", "params": {"threshold": 0.5}}], "slices": ["target_color"],
                   "coordinate_convention": "pixels, origin top-left, [x1,y1,x2,y2], original image size"}),
    ]
    for spec in specs:
        card = {**SYNTH_CARD, **spec.pop("card")}
        await create_dataset(db, card=card, builtin=True, synthetic=True, **spec)
