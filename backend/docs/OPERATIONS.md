# راه‌اندازی و عملیات — Operations

## راه‌اندازی اول (سرور جدید)

پیش‌نیاز: Docker با Compose v2، یک دامنه (برای گواهی خودکار) یا شبکه‌ی داخلی.

```bash
cd backend/deploy
./init-secrets.sh                 # رمزها و کلیدها را می‌سازد؛ مقدار قبلی را بازنویسی نمی‌کند
cp .env.example .env && nano .env # SITE_ADDRESS، آدرس سرویس‌های بیرونی، سیاست‌ها
# توکن سرویس‌ها:  secrets/inquiry_token  و  secrets/sms_token
docker compose up -d --build
docker compose run --rm api create-admin --username admin   # رمز یک‌بارمصرف چاپ می‌شود
docker compose run --rm api seed-places --gates "احمدبن‌موسی,باب‌الرضا,باب‌السجاد"
docker compose run --rm -v "$PWD/../../labels/output:/in:ro" api import-tents --file /in/tent_ids.csv
```

سپس با کاربر `admin` وارد شوید، رمز را عوض کنید، درب‌های هر گیت را (`POST /admin/places/{id}/doors`) و کاربران را بسازید.
فایل ساخت وب‌اپ (PWA) را در `deploy/pwa/` بگذارید (یا `PWA_DIR`).

> `import-tents` با تکرار بی‌خطر است (چادرهای موجود را رد می‌کند).

### اگر Docker Hub در دسترس نیست
نام imageها در `.env` قابل تغییر است (`GO_IMAGE`, `RUNTIME_IMAGE`, `POSTGRES_IMAGE`, `REDIS_IMAGE`, `CADDY_IMAGE`).
imageها را یک‌بار روی ماشینی که دسترسی دارد بکشید، به رجیستری داخلی push کنید و آدرس آن را بگذارید؛ یا
`docker save` / `docker load` برای انتقال دستی.

## به‌روزرسانی

```bash
git pull
cd deploy && docker compose up -d --build   # مایگریشن خودکار در سرویس migrate اجرا می‌شود
```
در ایام پرتردد به‌روزرسانی نکنید. مایگریشن‌ها فقط «افزودنی» نوشته شوند تا نسخه‌ی قبلی هم‌زمان کار کند.
جدول جدید لازم است به نقش `chador_app` دسترسی بگیرد؛ این کار را `migrate` خودکار انجام می‌دهد.

## پشتیبان‌گیری و بازیابی

```bash
./backup.sh                                   # فایل در deploy/backups، نگهداری ۱۴ روز (KEEP_DAYS)
BACKUP_PASSPHRASE_FILE=/root/bk.pass ./backup.sh   # با رمزنگاری اضافه
./restore-drill.sh backups/chador-YYYYMMDD-HHMMSS.dump   # آزمایش بازیابی روی پایگاه موقت
```
- زمان‌بندی پیشنهادی: هر ۳ ساعت (crontab در سرصفحه‌ی `backup.sh`).
- **فایل پشتیبان را از سرور خارج کنید** (دیسک دیگر/سرور دیگر). پشتیبانِ هم‌دیسک در خرابی دیسک از بین می‌رود.
- **هر ماه یک بار `restore-drill.sh` را اجرا کنید.** پشتیبانی که بازیابی‌اش آزموده نشده، پشتیبان نیست.
- داده‌ی شخصی داخل پشتیبان رمزشده است ولی **کلیدها در آن نیست**؛ `secrets/pii_*_key` را جدا و امن نگه دارید.

## هشدارها و نظارت

- `docker compose ps` — همه‌ی سرویس‌ها باید `healthy`/`running` باشند.
- `GET /readyz` را با ابزار پایش (مثلاً Uptime Kuma) هر ۳۰ ثانیه بررسی کنید.
- متریک‌ها: پورت `9090` داخل شبکه‌ی `app`؛ برای Prometheus همان شبکه را به آن بدهید.
  مهم‌ترین‌ها: `chador_http_requests_total{code="5xx"}`، `chador_http_request_seconds_*`، `chador_db_pool_acquired` نزدیک `chador_db_pool_max`.
- لاگ‌ها JSON هستند: `docker compose logs -f api worker`. هر خطا `request_id` دارد.
- پیامک‌های ناموفق: `SELECT state, count(*) FROM sms_log GROUP BY 1;`

## مشکل‌یابی سریع

| نشانه | بررسی |
|---|---|
| همه‌ی کاربران «خطای داخلی» می‌گیرند | `docker compose logs api` — معمولاً اتصال دیتابیس/Redis؛ `/readyz` را ببینید |
| ثبت زائر «در انتظار تأیید» می‌ماند | سرویس استعلام قطع است؛ job تلاش مجدد خودکار دارد (تا ۱۰ بار). `ALLOW_UNVERIFIED` فقط با تصمیم مدیریت |
| پیامک نمی‌رود | `sms_log.error`؛ توکن/آدرس `SMS_URL`؛ worker در حال اجراست؟ |
| «تلاش‌های ناموفق زیاد» برای یک کاربر | ۱۵ دقیقه صبر، یا `docker compose exec redis redis-cli -a ... DEL lock:u:<username>` |
| کاربر خروج‌شده/غیرفعال هنوز وارد است | نباید ممکن باشد؛ `RevokeUser` همه‌ی نشست‌ها را پاک می‌کند. اگر دیدید باگ است، گزارش دهید |
| پر شدن دیسک | `docker system df`؛ حجم `pgdata`؛ پشتیبان‌های قدیمی؛ لاگ‌ها محدود به ۵×۱۰ مگابایت‌اند |

## چرخه‌ی نگهداری داده

- worker هر شب ۰۳:۰۰ (به وقت تهران) زائرانی را که چادر در اختیار ندارند و `RETENTION_DAYS` روز دیده نشده‌اند ناشناس می‌کند.
- `audit_log` و `tent_events` هرگز پاک نمی‌شوند (فقط‌افزودنی). اگر حجمشان زیاد شد، پارتیشن‌بندی/آرشیو را با نقش مالک و خارج از برنامه انجام دهید.
- `client_ops` (شناسه‌ی عملیات تکراری) پس از ۷ روز پاک می‌شود.

## واکنش به رخداد

1. **گوشی/حساب لو رفته:** `PATCH /admin/users/{id} {"active": false}` — همه‌ی نشست‌های او در همان لحظه باطل می‌شود. سپس `audit_log` را برای فعالیت او بررسی کنید.
2. **کلید رمزنگاری لو رفته:** ابزار چرخش کلید هنوز ساخته نشده (محدودیت شناخته‌شده در `SECURITY.md`). تا آن زمان: سرویس را متوقف کنید، با توسعه‌دهنده هماهنگ کنید.
3. **ظن به دستکاری داده:** `tent_events`/`audit_log` فقط‌افزودنی‌اند و با نقش اجرا قابل تغییر نیستند؛ تفاوت را با آخرین پشتیبان مقایسه کنید.

## تنظیمات مهم (`deploy/.env`)

| متغیر | پیش‌فرض | معنی |
|---|---|---|
| `ALLOW_UNVERIFIED` | `false` | اجازه‌ی تحویل چادر در حالتی که استعلام در دسترس نیست |
| `UNDO_WINDOW` | `3m` | مهلت لغوِ تخصیص توسط خود خادم |
| `REMINDER_AFTER` | `6h` | زمان اولین پیامک یادآوری عودت |
| `MAX_REMINDERS` | `2` | حداکثر پیامک یادآوری برای هر تخصیص |
| `RETENTION_DAYS` | `90` | مدت نگهداری داده‌ی شخصی زائرِ غیرفعال |
| `ACCESS_TTL` / `REFRESH_TTL` | `15m` / `12h` | عمر توکن‌ها (۱۲ ساعت ≈ یک شیفت) |

قیمت جوراب، حداکثر چادر در هر درخواست و متن پیامک‌ها از طریق `PUT /admin/settings/{key}` (نیاز به redeploy ندارد).
