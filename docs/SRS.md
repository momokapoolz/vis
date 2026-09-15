# Software Requirements Specification

## Digital Wallet & Double-Entry Ledger Engine PoC

| Thuộc tính | Giá trị |
|---|---|
| Phiên bản tài liệu | 1.0 |
| Ngày ban hành | 15/09/2026 |
| Trạng thái | As-built baseline |
| Hệ thống | Wallet & Double-Entry Ledger PoC |
| Ngôn ngữ triển khai | Go |
| Cơ sở dữ liệu | PostgreSQL 17 |

## 1. Giới thiệu

### 1.1. Mục đích

Tài liệu này mô tả yêu cầu phần mềm của backend ví điện tử giả lập đã được triển khai trong repository. Tài liệu là cơ sở để:

- xác nhận phạm vi và hành vi hiện tại của PoC;
- review nghiệp vụ, bảo mật và tính đúng của ledger;
- xây dựng test case và nghiệm thu;
- đánh giá thay đổi ở các phiên bản tiếp theo.

Đây là tài liệu **as-built**: yêu cầu được rút ra từ mã nguồn, database migration, OpenAPI, tài liệu kiến trúc và kịch bản kiểm thử hiện có. Khi tài liệu này khác với ý tưởng ban đầu trong `setup.md`, hành vi đã triển khai và OpenAPI là baseline của phiên bản 1.0.

### 1.2. Phạm vi sản phẩm

Hệ thống cung cấp REST API để:

- đăng ký, đăng nhập và làm mới access token;
- tạo và tra cứu ví VND;
- nạp tiền giả lập qua webhook có chữ ký HMAC;
- chuyển tiền giữa các ví;
- tính số dư từ double-entry ledger;
- tra cứu giao dịch;
- hoàn tác toàn bộ một giao dịch bằng reversing entries;
- đối soát tính toàn vẹn của ledger;
- tra cứu user, ví và giao dịch theo vai trò.

Hệ thống không xử lý tiền thật và không phải một nền tảng thanh toán production.

### 1.3. Đối tượng đọc

- Product owner và người nghiệm thu PoC.
- Backend developer và database engineer.
- QA engineer.
- Security reviewer và vận hành hệ thống local/demo.

### 1.4. Tài liệu tham chiếu

- [Đặc tả khởi đầu](setup.md).
- [Kiến trúc và quy ước ledger](architecture.md).
- OpenAPI được nhúng tại `internal/wallet/openapi.json` và phục vụ qua `GET /openapi.json`.
- Hướng dẫn cài đặt, chạy demo và kiểm thử tại `README.md` ở thư mục gốc.

### 1.5. Thuật ngữ

| Thuật ngữ | Định nghĩa |
|---|---|
| Wallet | Ví lưu quyền sở hữu và currency; không lưu balance trực tiếp. |
| Ledger entry | Bút toán debit hoặc credit gắn với một transaction và một wallet. |
| Transaction | Giao dịch tài chính gồm đúng một debit và một credit có cùng amount. |
| Debit | Bút toán làm giảm số dư ví theo quy ước của PoC. |
| Credit | Bút toán làm tăng số dư ví theo quy ước của PoC. |
| Provider Clearing | Ví hệ thống làm đối ứng cho các khoản nạp giả lập. |
| Reversal | Transaction mới đảo chiều debit/credit của transaction gốc. |
| Idempotency | Cơ chế bảo đảm retry cùng một yêu cầu không tạo thêm hiệu ứng nghiệp vụ. |
| Reconciliation | Quá trình kiểm tra các invariant của ledger và báo cáo bất thường. |
| Customer | Người dùng cuối quản lý ví và chuyển tiền của chính mình. |
| Support | Nhân viên có quyền tra cứu toàn hệ thống, không có quyền reversal/reconciliation. |
| Admin | Nhân viên có quyền Support và quyền reversal/reconciliation. |

## 2. Mô tả tổng thể

### 2.1. Bối cảnh sản phẩm

Hệ thống là một Go HTTP monolith sử dụng PostgreSQL làm nguồn dữ liệu duy nhất. HTTP handler xác thực request và gọi trực tiếp service nghiệp vụ. Service thực thi SQL bằng `pgx`, bao gồm transaction, row lock và ledger posting. Không có frontend, ORM, Redis, message broker hoặc external payment provider.

Các thành phần runtime:

| Thành phần | Trách nhiệm |
|---|---|
| API process | Phục vụ REST API, JWT authentication, RBAC và nghiệp vụ ví. |
| PostgreSQL | Lưu dữ liệu, khóa đồng thời, kiểm tra constraint và bảo vệ ledger bất biến. |
| Migration command | Tạo schema, trigger, database grants và Provider Clearing wallet. |
| Seed command | Tạo tài khoản customer, support hoặc admin từ môi trường vận hành. |
| Mock-deposit command | Tạo và gửi webhook nạp tiền đã ký HMAC. |
| Reconcile command | Chạy đối soát và trả exit code thất bại khi có anomaly. |
| Windows Task Scheduler | Gọi reconcile command hằng ngày lúc 00:00 theo cấu hình local. |

### 2.2. Actor và quyền

| Chức năng | Anonymous | Customer | Support | Admin | Mock provider |
|---|:---:|:---:|:---:|:---:|:---:|
| Health, OpenAPI, Swagger UI | Có | Có | Có | Có | Có |
| Register, login, refresh | Có | Có | Có | Có | Không áp dụng |
| Tạo ví | Không | Có | Có | Có | Không |
| Xem ví và giao dịch của mình | Không | Có | Có | Có | Không |
| Tra cứu toàn bộ user/ví/giao dịch | Không | Không | Có | Có | Không |
| Chuyển từ ví thuộc sở hữu của mình | Không | Có | Có | Có | Không |
| Gửi deposit webhook | Không | Không | Không | Không | Có, bằng HMAC |
| Reverse transaction | Không | Không | Không | Có | Không |
| Chạy và xem reconciliation | Không | Không | Không | Có | Không |

Support và Admin vẫn phải sở hữu source wallet nếu gọi transfer. Hệ thống không cấp ví hoặc tiền đặc biệt cho tài khoản staff.

### 2.3. Môi trường vận hành

- Go 1.24 trở lên; container build sử dụng Go 1.26.
- PostgreSQL 17.
- Docker Desktop với Linux containers cho môi trường Windows được tài liệu hóa.
- API mặc định lắng nghe tại `:8080`.
- API và PostgreSQL trong Compose chỉ publish lên loopback của máy host.

### 2.4. Giả định và phụ thuộc

- PostgreSQL là nơi duy nhất được phép tạo thay đổi tài chính bền vững.
- Tất cả đường ghi tiền của ứng dụng phải đi qua ledger service và cùng quy tắc khóa.
- Đồng hồ hệ điều hành đủ chính xác để kiểm tra thời hạn JWT và refresh token.
- Scheduler của host chịu trách nhiệm gọi reconciliation command; API không chứa scheduler nội bộ.
- Swagger UI cần Internet để tải static assets từ CDN. OpenAPI JSON vẫn hoạt động offline.
- Secret và database URL được cấp qua environment variables.

### 2.5. Ngoài phạm vi phiên bản 1.0

- Tiền thật, kết nối ngân hàng hoặc payment provider thật.
- Đa tiền tệ và chuyển đổi tỷ giá.
- Hoàn tiền một phần hoặc nhiều lần cho cùng transaction.
- Balance cache, materialized balance hoặc ledger partitioning.
- Frontend, email, notification và hệ thống alert.
- Password reset, email verification, MFA, logout và revoke toàn bộ phiên đăng nhập.
- API thay đổi role, khóa/mở ví hoặc sửa thông tin user.
- Cleanup tự động cho idempotency key, webhook event và refresh token hết hạn.
- Cloud deployment, autoscaling, backup/restore và disaster recovery.

## 3. Quy tắc nghiệp vụ

| ID | Yêu cầu |
|---|---|
| BR-001 | Currency duy nhất của phiên bản 1.0 là `VND`. |
| BR-002 | Amount phải là JSON integer dương và nằm trong miền `int64`. Không nhận số thực. |
| BR-003 | Số dư ví bằng tổng credit trừ tổng debit; không được lưu hoặc cập nhật bằng cột `wallet.balance`. |
| BR-004 | Mỗi transaction tài chính phải có đúng hai ledger entries: một debit và một credit. |
| BR-005 | Hai entries của một transaction phải có cùng amount và thuộc hai wallet khác nhau. |
| BR-006 | Transaction và hai entries phải commit atomically; lỗi ở bất kỳ bước nào phải rollback toàn bộ. |
| BR-007 | Ledger entries và transactions đã post không được sửa hoặc xóa. |
| BR-008 | Customer wallet không được có số dư âm sau một money movement. Provider Clearing được phép âm. |
| BR-009 | Source và destination của transfer phải khác nhau, đang active và destination phải là customer wallet. |
| BR-010 | Người gọi transfer phải sở hữu source wallet. |
| BR-011 | Deposit phải debit Provider Clearing và credit một customer wallet. |
| BR-012 | Reversal phải tạo transaction mới, giữ nguyên transaction và entries gốc. |
| BR-013 | Chỉ được reversal toàn bộ một transaction `deposit` hoặc `transfer`, tối đa một lần. Không reversal một reversal. |
| BR-014 | Wallet bị debit trong reversal phải đủ số dư nếu đó là customer wallet. |
| BR-015 | Một user có thể sở hữu nhiều customer wallet; system wallet không có owner. |
| BR-016 | Hệ thống chỉ có một Provider Clearing wallet trong phiên bản 1.0. |
| BR-017 | Register công khai luôn tạo role `customer`; client không được tự chọn role. |
| BR-018 | Support và Admin có thể tra cứu toàn hệ thống; chỉ Admin có thể reversal và reconciliation. |

## 4. Yêu cầu chức năng

### 4.1. Authentication và user

#### FR-AUTH-001 — Đăng ký customer

Hệ thống phải cung cấp `POST /auth/register` với `email` và `password`.

- Email được trim, chuyển lowercase, kiểm tra định dạng và giới hạn 254 bytes.
- Password phải dài từ 8 đến 72 bytes.
- Password được lưu dưới dạng bcrypt digest.
- Email phải duy nhất.
- User mới luôn có role `customer`.
- Thành công trả HTTP `201` cùng user; email trùng trả `409`.

#### FR-AUTH-002 — Đăng nhập

Hệ thống phải cung cấp `POST /auth/login`.

- Credential đúng trả access token, refresh token, token type và thời hạn access token.
- Credential sai trả `401` với cùng một thông báo cho email không tồn tại và password sai.
- Access token là JWT HS256, issuer `wallet-poc`, audience `wallet-api`, hạn 15 phút.
- Refresh token là 32 random bytes biểu diễn dạng hex và hạn 7 ngày.
- Database chỉ lưu SHA-256 digest của refresh token.

#### FR-AUTH-003 — Làm mới token

Hệ thống phải cung cấp `POST /auth/refresh`.

- Refresh token hợp lệ được revoke và thay bằng cặp token mới trong cùng DB transaction.
- Mỗi refresh token chỉ được sử dụng thành công một lần.
- Token hết hạn, đã dùng hoặc không hợp lệ trả `401`.
- Hai request đồng thời dùng cùng refresh token chỉ có một request thành công.

#### FR-AUTH-004 — Xác thực API được bảo vệ

- Client phải gửi `Authorization: Bearer <JWT>`.
- Hệ thống phải kiểm tra chữ ký HS256, issuer, audience, issued-at và expiration.
- Hệ thống phải đọc user và role hiện tại từ database sau khi xác thực JWT.
- User không còn tồn tại hoặc token sai/hết hạn trả `401`.

#### FR-AUTH-005 — Seed tài khoản nội bộ

CLI `wallet seed` phải tạo user theo `SEED_EMAIL`, `SEED_PASSWORD` và `SEED_ROLE`.

- Role hợp lệ gồm `customer`, `support`, `admin`.
- Nếu email đã tồn tại cùng role, command trả user hiện tại và không đổi password.
- Nếu email đã tồn tại với role khác, command phải báo xung đột.
- Chức năng này sử dụng migration database account và không được cung cấp qua HTTP API.

#### FR-USER-001 — Tra cứu user

- Support/Admin có thể gọi `GET /users` với pagination và bộ lọc email exact-match sau normalize.
- Support/Admin có thể gọi `GET /users/{id}`.
- Response không được chứa password digest hoặc refresh token.
- Customer gọi các endpoint này phải nhận `403`.

### 4.2. Wallet

#### FR-WALLET-001 — Tạo wallet

Hệ thống phải cung cấp `POST /wallets` cho user đã đăng nhập.

- Request phải có `Idempotency-Key`.
- `name` là tùy chọn và tối đa 100 bytes.
- `currency` mặc định là `VND`; giá trị khác phải bị từ chối.
- Wallet được tạo là `customer`, `active` và thuộc user gọi request.
- Thành công trả HTTP `201`.

#### FR-WALLET-002 — Danh sách wallet

- `GET /wallets` trả các wallet user được phép nhìn thấy.
- Customer chỉ thấy wallet của mình.
- Support/Admin thấy toàn bộ wallet và có thể lọc theo `user_id`.
- Danh sách sắp xếp theo ID giảm dần.

#### FR-WALLET-003 — Chi tiết wallet

- `GET /wallets/{id}` trả wallet nếu actor có quyền.
- Customer yêu cầu wallet không thuộc mình phải nhận `404` để không tiết lộ sự tồn tại.

#### FR-WALLET-004 — Balance

- `GET /wallets/{id}/balance` phải tính balance từ ledger tại thời điểm request.
- Response phải gồm `wallet_id`, `currency: VND` và `balance` dạng `int64`.
- Hệ thống phải trả xung đột nếu kết quả tổng vượt miền `int64`.
- Quyền truy cập giống FR-WALLET-003.

### 4.3. Transfer và ledger posting

#### FR-TRANSFER-001 — Chuyển tiền

Hệ thống phải cung cấp `POST /transfers` với:

```json
{
  "source_wallet_id": 1,
  "destination_wallet_id": 2,
  "amount": 100000
}
```

- Request phải có JWT và `Idempotency-Key`.
- Input phải thỏa BR-002, BR-008, BR-009 và BR-010.
- Thành công tạo một transaction loại `transfer`, debit source và credit destination.
- Thành công trả HTTP `201` cùng transaction ID, hai wallet ID, amount và currency.
- Không đủ tiền trả `409` với code `insufficient_funds`.

#### FR-LEDGER-001 — Ghi sổ tập trung

- Deposit, transfer và reversal phải dùng chung ledger posting service.
- Handler không được tự ghi ledger entries.
- Hệ thống phải kiểm tra overflow cho số dư source và destination trước khi ghi.

#### FR-LEDGER-002 — Atomicity và database constraints

- Transaction row, debit entry và credit entry phải nằm trong cùng DB transaction.
- Deferred database constraint trigger phải kiểm tra đúng hai entries, hai phía, hai wallet và tổng bằng 0 tại commit.
- Unique constraints phải ngăn hai debit, hai credit hoặc hai entries cùng wallet trong một transaction.
- Database trigger phải từ chối UPDATE/DELETE trên `ledger_entries` và `transactions`.

#### FR-LEDGER-003 — Chống overspending đồng thời

- Hệ thống phải khóa tất cả wallet liên quan bằng `SELECT ... FOR UPDATE` trong DB transaction.
- Wallet phải được khóa theo ID tăng dần để giảm deadlock.
- Balance phải được đọc bằng statement riêng sau khi đã lấy đủ lock.
- Isolation level là PostgreSQL `READ COMMITTED`.
- Lock timeout là 5 giây.
- Với balance 100.000 và hai transfer đồng thời 80.000 từ cùng source, đúng một request được phép thành công.

### 4.4. Idempotency

#### FR-IDEM-001 — Phạm vi

`Idempotency-Key` bắt buộc với:

- `POST /wallets`;
- `POST /transfers`;
- `POST /admin/transactions/{id}/reverse`;
- `POST /admin/reconciliations`.

Auth không yêu cầu key. Webhook sử dụng `event_id` riêng.

#### FR-IDEM-002 — Định danh và hash

- Key dài 1–128 bytes và không có whitespace đầu/cuối.
- Key unique theo cặp `(user_id, key)` trên toàn bộ endpoint nghiệp vụ.
- Request hash phải bao gồm HTTP method, path và payload JSON đã parse rồi serialize chuẩn hóa.

#### FR-IDEM-003 — Replay và conflict

- Lần đầu phải claim key bằng atomic insert có unique constraint.
- Cùng key/cùng request phải trả nguyên HTTP status và response body đã lưu mà không chạy lại nghiệp vụ.
- Cùng key/khác method, path hoặc payload phải trả `409` với code `idempotency_conflict`.
- Request đồng thời cùng key phải chờ kết quả của request đã claim key.

#### FR-IDEM-004 — Transaction boundary

- Idempotency record, hiệu ứng nghiệp vụ và response phải commit trong cùng DB transaction.
- Business error xảy ra sau khi claim key phải được lưu và replay.
- Auth/validation error xảy ra trước khi claim key không được lưu.
- Infrastructure error phải rollback key và mọi hiệu ứng, cho phép retry cùng key.

### 4.5. Mock deposit webhook

#### FR-WEBHOOK-001 — Xác thực HMAC

Hệ thống phải cung cấp `POST /webhooks/mock-provider`.

- Provider gửi `X-Signature` là hex HMAC-SHA256 của exact raw request body dùng `WEBHOOK_SECRET`.
- Hệ thống phải verify chữ ký bằng constant-time comparison trước khi parse JSON.
- Chữ ký sai hoặc thiếu trả `401` và không thay đổi ledger.

#### FR-WEBHOOK-002 — Xử lý deposit

Payload hợp lệ:

```json
{
  "event_id": "evt_001",
  "event": "deposit.success",
  "wallet_id": 1,
  "amount": 500000
}
```

- Chỉ nhận event `deposit.success`.
- `event_id` dài 1–128 bytes, không có whitespace đầu/cuối.
- Destination phải là customer wallet active.
- Thành công tạo transaction loại `deposit`, debit Provider Clearing và credit destination.
- `event_id` phải được lưu vào `transactions.reference_id`.
- Thành công trả HTTP `200`.

#### FR-WEBHOOK-003 — Chống duplicate event

- `provider_event_id` phải unique.
- Event ID trùng cùng canonical payload phải replay response đã lưu.
- Event ID trùng khác payload phải trả `409` với code `event_conflict`.
- Event record, deposit transaction và response phải commit atomically.

#### FR-WEBHOOK-004 — Mock provider CLI

CLI `wallet mock-deposit WALLET_ID AMOUNT EVENT_ID` phải tạo JSON, ký HMAC và gửi đến `${API_URL}/webhooks/mock-provider`; `API_URL` mặc định là `http://localhost:8080`.

### 4.6. Transaction query

#### FR-TXN-001 — Chi tiết transaction

- `GET /transactions/{id}` trả transaction cùng ledger entries.
- Customer chỉ được xem transaction có ít nhất một wallet thuộc mình.
- Support/Admin được xem mọi transaction.
- Dữ liệu không tồn tại hoặc không có quyền nhìn phải trả `404`.

#### FR-TXN-002 — Lịch sử wallet

- `GET /wallets/{id}/transactions` trả các transaction có entry thuộc wallet.
- Mỗi item phải thể hiện entry type và amount của wallet đang xem.
- Danh sách sắp xếp theo transaction ID giảm dần và áp dụng pagination.
- Quyền truy cập giống wallet detail.

### 4.7. Reversal/refund

#### FR-REV-001 — Tạo reversal

Admin phải có thể gọi `POST /admin/transactions/{id}/reverse` với JWT, `Idempotency-Key` và body:

```json
{"reason":"Customer refund"}
```

- `reason` sau trim phải dài 1–500 bytes.
- Hệ thống phải khóa transaction gốc trước khi kiểm tra và ghi reversal.
- Debit của giao dịch gốc trở thành credit; credit của giao dịch gốc trở thành debit.
- Amount phải giữ nguyên.
- Transaction mới phải có loại `reversal`, `reversed_transaction_id`, actor và reason.
- Thành công trả HTTP `201`.

#### FR-REV-002 — Giới hạn reversal

- Transaction không tồn tại trả `404`.
- Reversal một reversal phải bị từ chối.
- Transaction đã reversal phải bị từ chối.
- Unique `reversed_transaction_id` phải chống hai reversal đồng thời.
- Nếu wallet customer cần debit không đủ tiền, yêu cầu phải trả `409` và không tạo entry.

### 4.8. Reconciliation

#### FR-RECON-001 — Chạy reconciliation

Admin phải có thể gọi `POST /admin/reconciliations` với body `{}` và `Idempotency-Key`.

Hệ thống phải kiểm tra trong một SQL statement/snapshot:

- transaction không có đúng hai entries;
- transaction không có đúng hai entry types;
- transaction không dùng hai wallet khác nhau;
- debit và credit của transaction không cân bằng;
- customer wallet có balance âm;
- tổng ledger toàn hệ thống không bằng 0.

#### FR-RECON-002 — Lưu report

- Mỗi lần chạy mới phải tạo một `reconciliation_runs` record.
- Status là `pass` nếu không có anomaly, ngược lại là `fail`.
- Report chứa danh sách `{kind, id, detail}`.
- Job chỉ báo cáo và không tự sửa dữ liệu.
- Admin có thể đọc report bằng `GET /admin/reconciliations/{id}`.

#### FR-RECON-003 — CLI và lịch chạy

- `wallet reconcile` phải gọi cùng reconciliation service với API.
- CLI phải in response JSON.
- CLI phải trả exit code khác 0 nếu có anomaly hoặc lỗi database.
- Script Windows phải hỗ trợ đăng ký task chạy lúc 00:00, timezone `SE Asia Standard Time`, chạy bù khi lỡ lịch và không chạy chồng.

### 4.9. Health và API documentation

#### FR-OPS-001 — Health endpoint

`GET /health` phải ping PostgreSQL. Database khả dụng trả `200 {"status":"ok"}`; lỗi database trả `503` theo error contract.

#### FR-OPS-002 — API contract

- `GET /openapi.json` phải trả OpenAPI 3.0.3 nhúng trong binary.
- `GET /docs` phải trả Swagger UI dùng `/openapi.json`.

## 5. Yêu cầu giao diện ngoài

### 5.1. HTTP và JSON

- API dùng HTTP/1.1 hoặc HTTP/2 do server/proxy cung cấp.
- Request body nghiệp vụ dùng `Content-Type: application/json`.
- Body phải là đúng một JSON object; unknown field, trailing value và type sai bị từ chối.
- Request body tối đa 1 MiB.
- Response JSON dùng `Content-Type: application/json` và `Cache-Control: no-store`.
- Server thêm `X-Content-Type-Options: nosniff` cho response qua handler chung.
- API timeout mỗi request sau 15 giây.

### 5.2. Pagination

- `limit` mặc định 20, miền hợp lệ 1–100.
- `offset` mặc định 0 và không âm.
- List response có cấu trúc `{ "items": [...] }`.

### 5.3. Error contract

Mọi lỗi do handler chung trả về có cấu trúc:

```json
{
  "error": {
    "code": "insufficient_funds",
    "message": "Insufficient funds"
  }
}
```

| HTTP status | Ý nghĩa |
|---:|---|
| 400 | JSON/input/header nghiệp vụ không hợp lệ. |
| 401 | JWT, credential, refresh token hoặc webhook signature không hợp lệ. |
| 403 | Actor đã xác thực nhưng thiếu quyền. |
| 404 | Resource không tồn tại hoặc không được phép nhìn thấy. |
| 409 | Xung đột nghiệp vụ, không đủ tiền, duplicate key/event khác payload hoặc reversal không hợp lệ. |
| 413 | Body vượt 1 MiB. |
| 503 | Lỗi hạ tầng/database tạm thời; response có `Retry-After: 1`. |

### 5.4. CLI

Binary phải hỗ trợ:

```text
wallet serve
wallet migrate
wallet seed
wallet mock-deposit WALLET_ID AMOUNT EVENT_ID
wallet reconcile
```

### 5.5. Environment variables

| Biến | Bắt buộc với | Yêu cầu |
|---|---|---|
| `DATABASE_URL` | serve, reconcile | PostgreSQL URL cho role ứng dụng. |
| `MIGRATION_DATABASE_URL` | migrate, seed | PostgreSQL URL cho schema owner. |
| `JWT_SECRET` | serve | Tối thiểu 32 bytes và khác webhook secret. |
| `WEBHOOK_SECRET` | serve, mock-deposit | Tối thiểu 32 bytes khi serve. |
| `LISTEN_ADDR` | serve | Tùy chọn, mặc định `:8080`. |
| `API_URL` | mock-deposit | Tùy chọn, mặc định `http://localhost:8080`. |
| `SEED_EMAIL` | seed | Email tài khoản cần seed. |
| `SEED_PASSWORD` | seed | Password 8–72 bytes. |
| `SEED_ROLE` | seed | `customer`, `support` hoặc `admin`. |
| `TEST_DATABASE_URL` | integration test | PostgreSQL superuser URL dùng để tạo database test biệt lập. |

## 6. Yêu cầu dữ liệu

### 6.1. Thực thể

| Bảng | Mục đích | Ràng buộc chính |
|---|---|---|
| `users` | User và role | Email lowercase/trim, unique; role enum. |
| `refresh_tokens` | Refresh token digest | Digest unique; expiry và revoked timestamp. |
| `wallets` | Customer/system wallet | VND; active/blocked; đúng quan hệ kind-owner. |
| `transactions` | Giao dịch đã post | Type deposit/transfer/reversal; reversal liên kết duy nhất. |
| `ledger_entries` | Debit/credit bất biến | Amount dương; unique side và wallet theo transaction. |
| `idempotency_keys` | Claim và replay request | Unique `(user_id, key)`; lưu hash/status/response. |
| `webhook_events` | Claim và replay provider event | Provider event ID unique; lưu payload/hash/response. |
| `reconciliation_runs` | Lịch sử đối soát | Status pass/fail và JSON report. |

### 6.2. Tính toàn vẹn

- Foreign keys phải bảo vệ quan hệ giữa user, wallet, transaction và ledger entry.
- Money dùng PostgreSQL `BIGINT` và Go `int64`.
- Trigger balance được deferred đến commit để cho phép insert transaction rồi insert hai entries trong cùng transaction.
- Application role chỉ được cấp SELECT/INSERT cần thiết và UPDATE theo cột cho refresh/idempotency/webhook records.
- Application role được UPDATE cột ID của wallet/transaction để PostgreSQL cho phép row lock; HTTP API không có chức năng thay đổi ID.

### 6.3. Lưu giữ dữ liệu

Phiên bản 1.0 không tự động xóa transaction, ledger entry, idempotency key, webhook event, reconciliation report hoặc refresh token. Ledger và transaction là dữ liệu bất biến theo thiết kế.

## 7. Yêu cầu phi chức năng

### 7.1. Bảo mật

| ID | Yêu cầu |
|---|---|
| NFR-SEC-001 | Password phải được hash bằng bcrypt; không trả digest qua API. |
| NFR-SEC-002 | Refresh token chỉ được lưu dưới dạng SHA-256 digest và phải rotate một lần. |
| NFR-SEC-003 | JWT và webhook secret phải khác nhau, mỗi secret tối thiểu 32 bytes khi API khởi động. |
| NFR-SEC-004 | JWT chỉ chấp nhận thuật toán HS256 và phải kiểm tra issuer/audience/expiration. |
| NFR-SEC-005 | Webhook signature phải so sánh constant-time trên raw body trước khi parse. |
| NFR-SEC-006 | Mọi endpoint protected phải thực thi RBAC và ownership ở server. |
| NFR-SEC-007 | API không được chạy bằng migration/schema-owner account. |
| NFR-SEC-008 | Response chứa token và dữ liệu API phải có `Cache-Control: no-store`. |
| NFR-SEC-009 | Service chỉ bind loopback trong cấu hình Docker Compose của PoC. |

### 7.2. Reliability và consistency

| ID | Yêu cầu |
|---|---|
| NFR-REL-001 | Money movement phải có ACID transaction và database-enforced balanced entries. |
| NFR-REL-002 | Retry sau khi mất response không được tạo money movement thứ hai khi dùng cùng key/event ID. |
| NFR-REL-003 | Concurrent transfer không được gây overspending. |
| NFR-REL-004 | Infrastructure error phải rollback cả dữ liệu nghiệp vụ và record chống lặp. |
| NFR-REL-005 | Server phải graceful shutdown với timeout 10 giây khi nhận interrupt/terminate. |

### 7.3. Hiệu năng và giới hạn tài nguyên

| ID | Yêu cầu |
|---|---|
| NFR-PERF-001 | Connection pool của API tối đa 10 PostgreSQL connections. |
| NFR-PERF-002 | Server read-header timeout 5 giây, read timeout 15 giây, write timeout 20 giây, idle timeout 60 giây. |
| NFR-PERF-003 | Mỗi API request qua handler chung có context timeout 15 giây. |
| NFR-PERF-004 | Row lock chờ tối đa 5 giây. |
| NFR-PERF-005 | Request body tối đa 1 MiB và HTTP headers tối đa 16 KiB. |
| NFR-PERF-006 | Balance được aggregate trực tiếp từ ledger; phiên bản 1.0 không cam kết throughput/latency SLA. |

Một Provider Clearing wallet làm các deposit đồng thời bị serialize trên cùng row lock. Đây là giới hạn được chấp nhận của PoC.

### 7.4. Observability và vận hành

| ID | Yêu cầu |
|---|---|
| NFR-OPS-001 | Process phải ghi structured JSON logs bằng `slog`. |
| NFR-OPS-002 | Lỗi không thuộc business error phải được log cùng method và path. |
| NFR-OPS-003 | Health endpoint phải phản ánh khả năng ping database. |
| NFR-OPS-004 | Reconciliation CLI phải dùng exit code để scheduler phát hiện fail/anomaly. |
| NFR-OPS-005 | Database migration phải có thể chạy độc lập trước API startup. |

### 7.5. Maintainability và portability

| ID | Yêu cầu |
|---|---|
| NFR-MAIN-001 | REST server ưu tiên Go standard library; PostgreSQL access dùng `pgx`. |
| NFR-MAIN-002 | Database schema được version hóa bằng Goose migration nhúng trong binary. |
| NFR-MAIN-003 | API contract được version hóa dưới dạng OpenAPI JSON trong repository. |
| NFR-MAIN-004 | Ứng dụng phải build được thành static Linux binary trong multi-stage Dockerfile. |
| NFR-MAIN-005 | CI phải chạy `go vet` và `go test -race` với PostgreSQL 17. |

## 8. Use case chính

### UC-01 — Tạo tài khoản và wallet

**Actor:** Customer.

**Tiền điều kiện:** Email chưa tồn tại.

**Luồng chính:**

1. Customer register bằng email/password.
2. Customer login và nhận access/refresh token.
3. Customer gọi `POST /wallets` với JWT và key mới.
4. Hệ thống tạo customer wallet VND có balance 0.

**Hậu điều kiện:** User và wallet tồn tại; ledger chưa có entry cho wallet mới.

### UC-02 — Nạp tiền giả lập

**Actor:** Mock provider.

**Tiền điều kiện:** Customer wallet tồn tại và active; provider có webhook secret.

**Luồng chính:**

1. Provider tạo deposit event với unique event ID.
2. Provider ký exact raw JSON body bằng HMAC-SHA256.
3. Hệ thống verify signature và claim event ID.
4. Hệ thống khóa clearing và destination wallet.
5. Hệ thống tạo balanced deposit transaction và lưu event response trong cùng commit.

**Luồng thay thế:** Event trùng cùng payload được replay; event trùng khác payload trả `409`; signature sai trả `401` và không ghi dữ liệu.

### UC-03 — Chuyển tiền an toàn

**Actor:** Customer.

**Tiền điều kiện:** Customer sở hữu source wallet; source đủ tiền; destination là customer wallet active.

**Luồng chính:**

1. Customer gửi transfer với JWT và idempotency key mới.
2. Hệ thống xác thực, kiểm tra ownership và claim key.
3. Hệ thống khóa hai wallet theo ID tăng dần.
4. Hệ thống tính source balance sau lock.
5. Hệ thống tạo transaction, debit source, credit destination và lưu response.
6. Hệ thống commit rồi trả `201`.

**Luồng thay thế:** Thiếu tiền trả/replay `409`; cùng key khác request trả `409`; lỗi DB trả `503` và rollback.

### UC-04 — Admin hoàn tác transaction

**Actor:** Admin.

**Tiền điều kiện:** Transaction gốc chưa reversal và wallet bị debit có đủ tiền nếu là customer wallet.

**Luồng chính:**

1. Admin gửi transaction ID, reason và idempotency key.
2. Hệ thống khóa transaction gốc.
3. Hệ thống kiểm tra loại, trạng thái reversal và entries gốc.
4. Hệ thống đảo hai entries trong transaction mới.
5. Hệ thống lưu liên kết, actor, reason và response trong cùng commit.

**Hậu điều kiện:** Transaction gốc không đổi; net financial effect của cặp original/reversal bằng 0.

### UC-05 — Đối soát ledger

**Actor:** Admin hoặc scheduler CLI.

**Luồng chính:**

1. Hệ thống chạy toàn bộ invariant checks trong một SQL statement.
2. Hệ thống tạo report `pass` hoặc `fail`.
3. Hệ thống lưu report và trả JSON.
4. CLI trả non-zero exit code khi report fail.

## 9. Tiêu chí nghiệm thu

| ID | Kịch bản | Kết quả mong đợi |
|---|---|---|
| AC-001 | Register/login đúng thông tin | Nhận JWT 15 phút và refresh token 7 ngày. |
| AC-002 | Dùng refresh token hai lần hoặc đồng thời | Chỉ một lần thành công; token cũ bị revoke. |
| AC-003 | Tạo wallet mới | Wallet VND active, balance tính từ ledger bằng 0. |
| AC-004 | Deposit 1.000.000 vào A | Clearing debit, A credit, A balance 1.000.000. |
| AC-005 | Transfer A → B 100.000 | A = 900.000, B = 100.000; transaction cân bằng. |
| AC-006 | Retry AC-005 bằng cùng key/payload | Response giống lần đầu và chỉ có một financial transaction. |
| AC-007 | Dùng key AC-005 với payload/path khác | HTTP 409, không có hiệu ứng mới. |
| AC-008 | A có 100.000; hai transfer đồng thời 80.000 | Đúng một request thành công, A còn 20.000. |
| AC-009 | Credit insert lỗi sau debit insert | Rollback transaction, entries và idempotency record. |
| AC-010 | Webhook signature sai | HTTP 401 và ledger không đổi. |
| AC-011 | Cùng event ID được gửi đồng thời | Cả hai nhận cùng kết quả; chỉ một deposit tồn tại. |
| AC-012 | Admin reverse transfer | Original không đổi; reversing entries tồn tại; net effect bằng 0. |
| AC-013 | Reverse cùng transaction hai lần | Một thành công, lần còn lại HTTP 409. |
| AC-014 | Reverse làm debit customer wallet không đủ tiền | HTTP 409 và không tạo reversal. |
| AC-015 | Customer đọc wallet/transaction người khác | HTTP 404. |
| AC-016 | Support reverse hoặc chạy reconciliation | HTTP 403. |
| AC-017 | Ledger bình thường được reconcile | Report `pass`, anomalies rỗng. |
| AC-018 | Database test có transaction lệch | Report `fail` và chứa anomaly tương ứng. |
| AC-019 | UPDATE/DELETE ledger hoặc transaction đã post | Database trigger từ chối. |
| AC-020 | `go vet` và unit tests | Hoàn thành không lỗi. |
| AC-021 | Integration suite với PostgreSQL thật | Hoàn thành không lỗi và không bị race khi chạy CI. |

### 9.1. Luồng demo nghiệm thu

1. Register/Login hai customer.
2. Tạo Wallet A và Wallet B.
3. Gửi signed deposit webhook 1.000.000 vào A.
4. Xác nhận A = 1.000.000.
5. Chuyển A → B 100.000.
6. Xác nhận A = 900.000 và B = 100.000.
7. Retry cùng idempotency key; xác nhận transaction ID không đổi.
8. Admin reverse transfer.
9. Xác nhận A = 1.000.000 và B = 0.
10. Chạy reconciliation và nhận status `pass`.

Script `scripts/demo.ps1` tự động hóa luồng này. Concurrent overspending được kiểm tra riêng trong integration suite.

## 10. Truy vết yêu cầu đến thành phần

| Nhóm yêu cầu | Thành phần triển khai | Kiểm chứng chính |
|---|---|---|
| FR-AUTH, FR-USER | `internal/wallet/auth.go`, `http.go` | Auth/RBAC integration scenarios. |
| FR-WALLET | `internal/wallet/http.go` | Wallet access, validation và balance scenarios. |
| FR-TRANSFER, FR-LEDGER | `internal/wallet/core.go`, migration | Atomicity, constraints và concurrent spending scenarios. |
| FR-IDEM | `internal/wallet/idempotency.go`, migration | Sequential/concurrent replay và conflict scenarios. |
| FR-WEBHOOK | `internal/wallet/webhook.go`, CLI | Known HMAC vector, invalid signature và duplicate event scenarios. |
| FR-TXN | `internal/wallet/http.go` | Ownership và staff-query scenarios. |
| FR-REV | `internal/wallet/core.go`, `http.go` | Double reversal, insufficient funds và race scenarios. |
| FR-RECON | `internal/wallet/core.go`, scripts | Pass report và corrupted-fixture scenarios. |
| FR-OPS | `internal/wallet/http.go`, OpenAPI | Unit validation và health check. |
| NFR-SEC | Auth/webhook handlers, Compose roles | Security-focused integration scenarios và config validation. |
| NFR-REL | Core service, database constraints | PostgreSQL integration/concurrency suite. |
| NFR-PERF | Server/pool/transaction config | Static configuration review. |
| NFR-MAIN | Dockerfile, migrations, CI workflow | Build, `go vet`, `go test -race`. |

## 11. Trạng thái xác minh tại baseline

Tại thời điểm phát hành tài liệu:

- `go test -count=1 -v ./...` đã pass các unit checks; integration suite tự động skip khi chưa có `TEST_DATABASE_URL`.
- `go vet ./...` đã pass.
- PowerShell scripts đã qua syntax parse.
- Docker Compose configuration đã qua `docker compose config --quiet`.
- PostgreSQL integration/concurrency suite đã được triển khai nhưng chưa chạy trên máy local của baseline vì không có PostgreSQL/Docker daemon hoạt động. CI đã được cấu hình để chạy suite với PostgreSQL 17.

Vì vậy, AC-021 chỉ được đánh dấu đạt sau khi integration suite chạy thành công trong CI hoặc môi trường PostgreSQL thật.

## 12. Quản lý thay đổi

Mọi thay đổi làm khác API, business rule, database invariant, RBAC, idempotency semantics hoặc reconciliation checks phải cập nhật đồng thời:

1. mã nguồn và migration nếu cần;
2. OpenAPI contract;
3. test/acceptance scenario;
4. tài liệu SRS này và version/date ở đầu tài liệu.

