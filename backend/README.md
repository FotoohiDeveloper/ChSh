# Chador — backend

سامانه‌ی رهگیری و مدیریت چادر زائران: Go + PostgreSQL + Redis، اجرا در Docker.
(Backend for the tent-tracking system: Go, PostgreSQL, Redis, Docker.)

- **یک باینری، سه نقش:** `serve` (API)، `worker` (پیامک/یادآوری/پاک‌سازی)، `migrate`/`create-admin`/`seed-places`/`import-tents`.
- **سریع:** تخصیص و بازگشت چندچادری در یک تراکنش؛ احراز هویت و محدودیت نرخ در یک رفت‌وبرگشت Redis؛ داشبورد با کش ۲ ثانیه‌ای.
- **امن:** داده‌ی هویتی رمزشده، لاگ فقط‌افزودنی، نقش دیتابیس بدون امتیاز، کانتینر بدون root/shell. جزئیات و **محدودیت‌ها** در [`docs/SECURITY.md`](docs/SECURITY.md).
- **آفلاین‌پذیر:** هر عملیات `op_id` دارد و `POST /sync` صف گوشی را بی‌خطر (حتی چندبار) اعمال می‌کند.

## اجرای سریع برای توسعه

```bash
make dev        # Postgres+Redis در Docker، API روی میزبان با سرویس‌های ساختگی
make race       # همه‌ی تست‌ها  (نیاز: متغیرهای TEST_*؛ پایین را ببینید)
```

## تست

```bash
make test                       # واحد، بدون پایگاه‌داده
export TEST_DATABASE_URL='postgres://chador:chador@localhost:5432/chador_test?sslmode=disable'
export TEST_REDIS_URL='redis://localhost:6379/9'
export TEST_ALLOW_RESET=1       # تست‌ها پایگاه‌داده‌ی بالا را پاک می‌کنند؛ فقط یک پایگاه‌داده‌ی موقت بدهید
make race                       # واحد + یکپارچه، با race detector
make loadtest                   # تست بار روی HTTP واقعی
make smoke                      # باینری واقعی با پیکربندی تولید (نیاز: psql, redis-cli, jq)
```

برای اجرای کل مجموعه با نقش **بدون‌امتیاز** (همان که در تولید است) علاوه بر بالا:
`TEST_APP_ROLE=chador_app TEST_APP_DATABASE_URL=postgres://chador_app@.../chador_test`.

## آنچه اندازه‌گیری شد (و آنچه نه)

محیط: یک sandbox ۴ هسته‌ای که **همزمان** سرور، Postgres، Redis و مولد بار را اجرا می‌کرد؛ Postgres با تنظیمات پیش‌فرض و fsync روشن.
۴۰ خادم هم‌زمان، چرخه‌ی «تخصیص ۲ چادر ← اسکن ← داشبورد ← بازگشت ۲ چادر» با ۱۵ms مکث بین کارها، ۸ ثانیه:

| endpoint | p50 | p95 | p99 |
|---|---|---|---|
| `POST /assignments` (۲ چادر) | 4.0 ms | 7.1 ms | 10.6 ms |
| `POST /returns` (۲ چادر) | 4.4 ms | 7.8 ms | 9.3 ms |
| `GET /tents/{code}` (اسکن) | 0.7 ms | 2.1 ms | 3.7 ms |
| `GET /dashboard` | 1.0 ms | 4.2 ms | 6.6 ms |

حدود ۲۲۰۰ درخواست در ثانیه بدون هیچ پاسخ غیرمنتظره. نیاز واقعی (ده‌ها خادم، هر کدام چند عملیات در دقیقه) چند برابر کمتر است.
این اعداد روی **سخت‌افزار شما** تکرار نشده‌اند؛ `make loadtest` را روی سرور مقصد اجرا کنید. تأخیر شبکه‌ی گوشی تا سرور جدا از این است.

## ساختار

```
cmd/chador/        main و زیردستورها
internal/domain/   نقش‌ها، ماشین وضعیت چادر، اعتبارسنجی (کد ملی، موبایل، تاریخ شمسی)
internal/service/  منطق کسب‌وکار (هر عملیات = یک تراکنش)
internal/api/      HTTP: مسیرها، احراز هویت، هدرهای امنیتی، SSE، متریک
internal/jobs/     صف Redis (asynq): پیامک، تأیید هویت مجدد، یادآوری، پاک‌سازی
internal/provider/ استعلام هویت و پیامک پشت interface، با timeout و circuit breaker
internal/platform/ تنظیمات، اتصال/مایگریشن دیتابیس، رمزنگاری
deploy/            Compose تولید، Caddy، تولید secrets، پشتیبان‌گیری
docs/              API.md، SECURITY.md، OPERATIONS.md
scripts/           smoke.sh (باینری واقعی)، stub_vendor.py
```

## پیش از استفاده‌ی واقعی

1. **سرویس استعلام هویت و پیامک:** `internal/provider/provider.go` یک کلاینت JSON عمومی دارد؛ قرارداد واقعی فروشنده را در `inquiryReq/inquiryResp` و `HTTPSMS` تنظیم کنید. تا آن موقع تولید عمداً با حالت `mock` بالا نمی‌آید.
2. **Docker واقعی:** `Dockerfile` و `docker-compose.yml` اعتبارسنجی نحوی شده‌اند ولی در محیط توسعه‌ی این پروژه روی daemon اجرا نشدند. یک بار روی سرور آزمایشی `make up` بزنید.
3. **تست نفوذ مستقل** و بازبینی حقوقی نگه‌داری داده‌ی هویتی (`docs/SECURITY.md`، بخش «پوشش نمی‌دهد»).
4. **وب‌اپ PWA** در این مخزن نیست؛ API آن را کامل پوشش می‌دهد (`docs/API.md`).
