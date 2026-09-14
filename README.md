# Wallet & Double-Entry Ledger PoC

Backend Go + PostgreSQL mô phỏng ví điện tử VND, theo [đặc tả ban đầu](docs/setup.md).
Không xử lý tiền thật. Số dư được tính từ ledger; mỗi giao dịch có một debit và một credit, commit cùng nhau.

## Chạy bằng Docker trên Windows

Cần Docker Desktop với Linux containers. Tại thư mục repo:

```powershell
Copy-Item .env.example .env
docker compose up --build -d
Invoke-RestMethod http://localhost:8080/health
```

Compose khởi tạo PostgreSQL, tạo role ứng dụng, chạy migrations rồi mở API.
DB và API chỉ publish lên loopback. `.env.example` chứa giá trị dành cho local; `.env` được gitignore.
Password trong connection URL phải được percent-encode nếu chứa ký tự đặc biệt.

- Swagger UI: http://localhost:8080/docs (cần Internet để tải assets Swagger).
- OpenAPI JSON: http://localhost:8080/openapi.json (có sẵn offline).
- Tắt dịch vụ và giữ dữ liệu: `docker compose down`.
- Role/password DB được tạo ở lần khởi tạo volume đầu tiên; sửa `.env` không tự thay password của DB đã tồn tại.

## Chạy Go trực tiếp

Cần Go >= 1.24; Docker/CI dùng Go 1.26. Dependencies được cố định trong `go.mod` và `go.sum`.

```powershell
docker compose up -d db
. .\scripts\load-env.ps1
go run ./cmd/wallet migrate
go run ./cmd/wallet serve
```

Nếu dùng PostgreSQL bên ngoài Compose, quản trị viên cần tạo role `wallet_app` có LOGIN/password và database trước.
`MIGRATION_DATABASE_URL` là tài khoản sở hữu schema; `DATABASE_URL` là `wallet_app`.
Không cấp tài khoản migration cho API đang chạy. CLI seed là thao tác quản trị local.

## Seed và demo toàn luồng

Tạo admin; email có sẵn cùng role sẽ được giữ nguyên, không tự đổi mật khẩu:

```powershell
$env:SEED_EMAIL = 'admin@example.com'
$env:SEED_PASSWORD = 'local-admin-password'
$env:SEED_ROLE = 'admin'
docker compose --profile tools run --rm seed

$env:ADMIN_EMAIL = $env:SEED_EMAIL
$env:ADMIN_PASSWORD = $env:SEED_PASSWORD
.\scripts\demo.ps1
```

Demo tạo hai customer với email ngẫu nhiên và hai ví, ký webhook nạp 1.000.000,
chuyển 100.000, retry cùng key, kiểm tra A/B = 900.000/100.000, reverse,
kiểm tra A/B = 1.000.000/0 rồi chạy reconciliation. Script thất bại nếu số dư không đúng.
Kịch bản concurrent transfer chạy riêng trong integration tests để không làm thay đổi số dư demo.

Tạo support bằng cùng lệnh seed với `SEED_ROLE=support` và email khác.
Nạp mock thủ công, dùng event ID mới cho một khoản nạp mới:

```powershell
. .\scripts\load-env.ps1
go run ./cmd/wallet mock-deposit 2 1000000 evt-manual-001
```

Thay `2` bằng ID ví customer thực tế. `API_URL` mặc định là `http://localhost:8080`.
Mock provider dùng HMAC-SHA256 dạng hex của **raw body**, header `X-Signature`.
Chỉ nhận `deposit.success`; event trùng cùng nội dung trả lại response cũ.

## Quy tắc API

| Nhóm | Hành vi |
|---|---|
| Auth | Register chỉ tạo customer; JWT HS256 hạn 15 phút; refresh token hạn 7 ngày, dùng một lần và rotate atomically. DB chỉ lưu digest refresh token và bcrypt password. |
| Wallet | Mỗi user có nhiều ví. Currency mặc định VND; không có API đổi chủ hoặc đổi currency. Ví clearing là system, không thể tạo qua API customer. |
| Quyền | Customer xem ví mình và giao dịch có ví mình tham gia. Support/admin tra cứu toàn hệ thống; chỉ admin reverse và đối soát. Staff chuyển tiền cũng phải dùng ví thuộc chính họ. |
| Tiền | JSON integer dương, trong giới hạn `int64`; không nhận float hoặc số vượt giới hạn. Client JavaScript phải giữ chính xác số nguyên lớn, không làm tròn qua `Number`. |
| Transfer | Hai ví phải khác nhau, active; source thuộc người gọi, destination là ví customer; không cho source customer âm. |
| Refund | Đảo toàn bộ một deposit/transfer, một lần. Ví customer bị debit phải đủ tiền; không đảo reversal; yêu cầu `reason`. Entries và transaction gốc giữ nguyên. |
| Danh sách | `limit=20`, tối đa 100, `offset=0`; thứ tự ID giảm dần; response `{ "items": [...] }`. Staff có thể lọc `/users?email=...`, `/wallets?user_id=...`. |

`Idempotency-Key` (1–128 bytes, không có khoảng trắng đầu/cuối) bắt buộc cho:

- `POST /wallets`
- `POST /transfers`
- `POST /admin/transactions/:id/reverse`
- `POST /admin/reconciliations` (body `{}`)

Key unique theo user **trên tất cả endpoint nghiệp vụ**. Hash bao gồm method, path và payload đã chuẩn hóa.
Cùng key/cùng request replay nguyên HTTP status/body; khác request trả `409`.
Lỗi nghiệp vụ sau khi claim key cũng được lưu: sau lỗi thiếu tiền, muốn tạo một yêu cầu mới phải dùng key mới.
Lỗi auth/validation trước khi claim và lỗi hạ tầng rollback không được lưu.
Request đang tranh chấp chờ tối đa 5 giây lấy lock; lỗi tạm trả `503` và `Retry-After: 1`.
Khi mất response hoặc nhận `503`, retry **cùng key** để xác định kết quả.

Auth không dùng key. Webhook dùng unique `event_id`; lỗi nghiệp vụ đã xử lý cũng được replay,
còn lỗi DB rollback cho phép retry. PoC giữ lịch sử keys/events, chưa có cleanup theo thời hạn.

Lỗi có dạng:

```json
{"error":{"code":"insufficient_funds","message":"Insufficient funds"}}
```

`400`: dữ liệu sai; `401`: auth/HMAC sai; `403`: thiếu quyền; `404`: không tồn tại hoặc không được thấy;
`409`: xung đột nghiệp vụ; `413`: body lớn hơn 1 MiB; `503`: lỗi DB tạm thời.

## Kiểm thử

Unit checks và static analysis:

```powershell
go test -v ./...
go vet ./...
```

**Nếu thiếu `TEST_DATABASE_URL`, integration suite sẽ báo SKIP.** Chạy đầy đủ bằng PostgreSQL thật:

```powershell
docker compose --profile tools run --build --rm test
```

Hoặc dùng Go local với PostgreSQL test đã chạy:

```powershell
$env:TEST_DATABASE_URL = 'postgres://wallet_migrator:local-migration-password@localhost:5432/postgres?sslmode=disable'
go test -count=1 -v ./...
```

Suite cần tài khoản superuser để tạo **database test riêng** `wallet_test_<timestamp>`, chạy migrations,
SET ROLE `wallet_app` cho các connection ứng dụng và cài dữ liệu hỏng để kiểm tra reconciliation.
Database test được drop khi xong; database trong URL chỉ dùng làm kết nối quản trị, không bị migrate hoặc xóa.
Nếu chưa có role `wallet_app`, suite tạo role NOLOGIN và giữ lại; không dùng server production để test.

Các kiểm tra gồm: balance/atomicity; debit rồi credit lỗi; DB không cho commit thiếu entry;
ledger không sửa/xóa; thiếu tiền; overspending đồng thời; transfer ngược chiều;
duplicate key/event đồng thời; payload xung đột; JWT/refresh/RBAC; refund hai lần hoặc cạnh tranh với transfer;
overflow; reconciliation nhận ra bút toán hỏng. Goroutines được đồng bộ điểm bắt đầu, dùng connection DB độc lập.

GitHub Actions chạy `go vet` và `go test -race` với PostgreSQL 17 khi push/PR.

## Reconciliation nightly

API admin và CLI cùng dùng một truy vấn snapshot để tìm giao dịch thiếu/sai cặp entries,
tổng debit/credit lệch và ví customer âm. Kết quả lưu trong `reconciliation_runs`.
Job chỉ báo cáo, không tự sửa lịch sử. CLI trả exit code khác 0 nếu có anomaly hoặc lỗi DB.

```powershell
.\scripts\reconcile.ps1
.\scripts\install-reconciliation-task.ps1
```

Lệnh thứ hai đăng ký task `WalletPoC-Reconciliation` lúc 00:00 theo giờ Windows;
script yêu cầu timezone `SE Asia Standard Time` (UTC+07:00, tương ứng Asia/Ho_Chi_Minh).
Task chạy dưới tài khoản hiện tại; Docker Desktop cần đang chạy và máy có phiên đăng nhập.
Task chạy bù khi bỏ lỡ lịch, không chạy chồng nhau. Trên host/timezone khác, cấu hình scheduler tương ứng gọi `wallet reconcile`.

Xem report qua `GET /admin/reconciliations/:id`; lỗi tiến trình nằm trong stderr/container log và Task Scheduler result.
Thiết kế DB và luồng khóa: [docs/architecture.md](docs/architecture.md).

## Giới hạn PoC

Một clearing wallet làm các deposit cùng tranh chấp một row lock. Balance được aggregate trực tiếp từ ledger.
Đây là lựa chọn ưu tiên tính đúng; chỉ thêm cache/snapshot hoặc tách clearing khi đã đo được giới hạn tải.
Chưa có frontend, tiền thật, hoàn một phần, email, notification, cloud deployment hay hệ thống alert.
