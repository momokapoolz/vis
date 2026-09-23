# Hướng dẫn chạy dự án local

Tài liệu này dành cho Windows PowerShell và Docker Desktop chạy Linux containers.

## 1. Các service

| Service | Vai trò | Chế độ |
|---|---|---|
| `db` | PostgreSQL 17, dữ liệu nằm trong volume `pgdata` | Mặc định |
| `migrate` | Chạy database migration rồi thoát | Mặc định |
| `api` | HTTP API tại `localhost:8080` | Mặc định |
| `seed` | Tạo user trực tiếp trong database | Profile `tools` |
| `test` | Chạy test với PostgreSQL thật | Profile `tools` |

Thứ tự khởi động:

```text
db healthy -> migrate hoàn thành -> api khởi động
```

`migrate` ở trạng thái `Exited (0)` là thành công. Đây là job chạy một lần, không phải server cần chạy liên tục.

---

## 2. Chuẩn bị

Cần cài:

- Docker Desktop chạy Linux containers.
- Docker Compose v2.
- PowerShell.
- Internet trong lần build đầu để tải Docker images và Go dependencies.

Kiểm tra:

```powershell
docker version
docker compose version
```

`docker version` phải hiển thị cả Client và Server.

Nếu chỉ hiện Client hoặc báo lỗi named pipe, Docker Desktop chưa chạy hoặc chưa sẵn sàng.

---

## 3. Chạy lần đầu

### 3.1. Vào thư mục dự án

```powershell
cd vis
```

### 3.2. Tạo file `.env`

```powershell
Copy-Item .env.example .env
```

Các nhóm biến chính:

| Biến | Mục đích |
|---|---|
| `POSTGRES_PASSWORD` | Mật khẩu role migration/owner |
| `APP_DB_PASSWORD` | Mật khẩu role `wallet_app` mà API sử dụng |
| `DATABASE_URL` | Kết nối DB bằng role ứng dụng từ máy host |
| `MIGRATION_DATABASE_URL` | Kết nối DB bằng role migration từ máy host |
| `JWT_SECRET` | Secret dùng để ký access token |
| `WEBHOOK_SECRET` | Secret dùng để xác thực webhook mock bằng HMAC |
| `SEED_EMAIL` | Email user được tạo bởi lệnh seed |
| `SEED_PASSWORD` | Mật khẩu user được tạo bởi lệnh seed |
| `SEED_ROLE` | Role cần tạo: `customer`, `support` hoặc `admin` |
| `ADMIN_EMAIL` | Email admin mà script demo sử dụng |
| `ADMIN_PASSWORD` | Mật khẩu admin mà script demo sử dụng |

`JWT_SECRET` và `WEBHOOK_SECRET` phải:

- Khác nhau.
- Dài ít nhất 32 byte.

Hai nhóm seed và admin phải khớp nhau:

```env
SEED_EMAIL=admin@example.com
SEED_PASSWORD=your-local-password
SEED_ROLE=admin

ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD=your-local-password
```

Cấu hình port hiện tại:

- PostgreSQL từ máy host: `localhost:5436`.
- PostgreSQL giữa các container: `db:5432`.
- API: `localhost:8080`.

Không đổi `db:5432` trong các URL nằm trong `compose.yaml` thành `5436`. Port `5436` chỉ dùng khi kết nối từ máy Windows vào container.

### 3.3. Kiểm tra line ending của shell script

File `scripts/init-db.sh` chạy bên trong Linux container nên phải dùng line ending `LF`, không phải `CRLF`.

Trong VS Code:

1. Mở `scripts/init-db.sh`.
2. Bấm `CRLF` ở góc dưới bên phải.
3. Chọn `LF`.
4. Lưu file.

Nếu dùng CRLF, log PostgreSQL sẽ báo:

```text
/bin/sh^M: bad interpreter: No such file or directory
```

Nên thêm file `.gitattributes` để Git luôn giữ shell script ở định dạng LF:

```gitattributes
*.sh text eol=lf
```

### 3.4. Build và khởi động

```powershell
docker compose up --build -d
```

Kiểm tra trạng thái:

```powershell
docker compose ps -a
```

Xem log migration:

```powershell
docker compose logs --no-color migrate
```

Kiểm tra API:

```powershell
Invoke-RestMethod http://localhost:8080/health
```

Kết quả mong đợi:

- `db`: `Up (healthy)`.
- `migrate`: `Exited (0)`.
- `api`: `Up`.
- `/health`: trả về `status = ok`.

Tài liệu API:

- Swagger UI: http://localhost:8080/docs
- OpenAPI JSON: http://localhost:8080/openapi.json

Swagger UI cần Internet để tải assets. OpenAPI JSON được nhúng trong binary nên dùng được offline.

### 3.5. Seed tài khoản admin

Chạy:

```powershell
docker compose --profile tools run --build --rm seed
```

Kết quả thành công là JSON user có role admin, ví dụ:

```json
{"id":1,"email":"admin@example.com","role":"admin"}
```

### 3.6. Chạy demo toàn luồng

```powershell
.\scripts\demo.ps1
```

Demo thực hiện:

1. Tạo hai customer ngẫu nhiên.
2. Đăng nhập hai customer.
3. Tạo hai ví.
4. Nạp mock `1.000.000 VND` vào ví A.
5. Chuyển `100.000 VND` từ A sang B.
6. Gửi lại cùng `Idempotency-Key`.
7. Xác nhận không tạo giao dịch thứ hai.
8. Đăng nhập admin.
9. Reverse giao dịch.
10. Kiểm tra số dư trở về trạng thái ban đầu.
11. Chạy reconciliation.
12. Yêu cầu kết quả reconciliation là `pass`.

Kết quả thành công:

```text
PASS: deposit, transfer, retry, reversal, reconciliation; ...
```

---

## 4. Chạy từ lần thứ hai trở đi

### Source không thay đổi

```powershell
docker compose up -d
```

Không cần build lại image.

### Source hoặc dependencies đã thay đổi

Nếu vừa sửa Go source, `Dockerfile`, `go.mod` hoặc `go.sum`:

```powershell
docker compose up --build -d
```

Kiểm tra nhanh:

```powershell
docker compose ps -a
Invoke-RestMethod http://localhost:8080/health
```

Không cần seed lại admin vì dữ liệu vẫn còn trong volume `pgdata`.

Có thể chạy lại demo:

```powershell
.\scripts\demo.ps1
```

Demo sử dụng email và idempotency key ngẫu nhiên nên có thể chạy nhiều lần trên cùng database.

---

## 5. Profile `tools` là gì?

`tools` là Docker Compose profile chứa các service chỉ chạy khi cần:

- `seed`
- `test`

Các service này không được chạy bởi lệnh thông thường:

```powershell
docker compose up -d
```

Muốn chạy chúng phải bật profile:

```powershell
docker compose --profile tools run --rm seed
docker compose --profile tools run --rm test
```

Mỗi service hiện được Compose build thành một image riêng, ví dụ:

```text
vis-api
vis-migrate
vis-seed
```

Vì vậy:

```powershell
docker compose up --build -d
```

có thể build lại `api` và `migrate`, nhưng không build lại `seed` vì `seed` không thuộc profile mặc định.

Sau khi sửa code được lệnh seed sử dụng, chạy:

```powershell
docker compose --profile tools run --build --rm seed
```

Ý nghĩa:

- `--profile tools`: bật nhóm service phụ trợ.
- `run`: chạy một container một lần.
- `--build`: build lại image từ source hiện tại.
- `--rm`: xóa container sau khi chạy.
- `seed`: tên service cần chạy.

---

## 6. Seed hoạt động thế nào?

Dockerfile khai báo:

```dockerfile
ENTRYPOINT ["wallet"]
```

Service `seed` trong `compose.yaml` khai báo:

```yaml
command: ["seed"]
```

Hai phần kết hợp thành lệnh:

```text
wallet seed
```

CLI thực hiện:

1. Đọc `SEED_EMAIL`.
2. Đọc `SEED_PASSWORD`.
3. Đọc `SEED_ROLE`.
4. Kết nối database bằng `MIGRATION_DATABASE_URL`.
5. Gọi `SeedUser`.
6. Thêm user vào bảng `users`.
7. In user vừa tạo ra stdout.

Seed hỗ trợ các role:

```text
customer
support
admin
```

### Seed không cập nhật password user cũ

Nếu email đã tồn tại, câu lệnh insert sử dụng:

```sql
ON CONFLICT(email) DO NOTHING
```

Điều này có nghĩa:

- Chạy lại cùng email và cùng role: giữ nguyên user và password cũ.
- Chạy lại cùng email nhưng role khác: báo conflict.
- Thay đổi `SEED_PASSWORD` trong `.env`: không tự đổi password user đã tồn tại.

Vì vậy `ADMIN_PASSWORD` phải là password được dùng trong lần seed đầu tiên, không chỉ là giá trị mới nhất trong `.env`.

Nếu quên password, cách đơn giản cho môi trường local là:

- Seed một admin bằng email mới; hoặc
- Reset toàn bộ database nếu không cần dữ liệu hiện tại.

---

## 7. `.env`, Docker Compose và PowerShell

Docker Compose tự đọc file `.env` để thay các biến trong `compose.yaml`, ví dụ:

```yaml
SEED_EMAIL: ${SEED_EMAIL:-}
```

Tuy nhiên, PowerShell không tự động biến các dòng trong `.env` thành biến `$env:...`.

Ví dụ, dòng sau trong `.env`:

```env
ADMIN_EMAIL=admin@example.com
```

không tự tạo ra:

```powershell
$env:ADMIN_EMAIL
```

Script `demo.ps1` xử lý việc này bằng cách dot-source:

```powershell
. .\scripts\load-env.ps1
```

`load-env.ps1` chỉ nạp các biến nằm trong allowlist `$walletNames`.

Cấu hình hiện tại phải có:

```powershell
'ADMIN_EMAIL', 'ADMIN_PASSWORD'
```

trong `$walletNames` để `demo.ps1` đọc được hai biến admin từ `.env`.

Có thể nạp thủ công các biến được hỗ trợ:

```powershell
. .\scripts\load-env.ps1
```

---

## 8. Chạy test

Chạy test đầy đủ với PostgreSQL:

```powershell
docker compose --profile tools run --build --rm test
```

Chạy static analysis bằng Go local:

```powershell
go vet ./...
```

Nếu chỉ chạy:

```powershell
go test ./...
```

mà không có `TEST_DATABASE_URL`, các integration test cần PostgreSQL có thể bị skip.

---

## 9. Dừng và khởi động lại

Dừng container nhưng giữ database:

```powershell
docker compose down
```

Khởi động lại:

```powershell
docker compose up -d
```

Xem log API:

```powershell
docker compose logs -f api
```

---

## 10. Reset toàn bộ database local

Chạy:

```powershell
docker compose down -v
docker compose up --build -d
docker compose --profile tools run --build --rm seed
```

`docker compose down -v` xóa:

- Container của dự án.
- Docker network của dự án.
- Volume `pgdata`.
- Toàn bộ user.
- Toàn bộ wallet.
- Toàn bộ transaction.
- Toàn bộ ledger data.

Chỉ sử dụng khi chắc chắn không cần dữ liệu local hiện tại.

---

## 11. Lỗi thường gặp

### `service "migrate" didn't complete successfully`

Đây chỉ là thông báo tổng quát. Xem lỗi gốc:

```powershell
docker compose ps -a
docker compose logs --no-color --tail 200 db
docker compose logs --no-color --tail 200 migrate
```

### `role "wallet_app" does not exist`

Role `wallet_app` được tạo bởi `scripts/init-db.sh` khi volume PostgreSQL được khởi tạo lần đầu.

Lỗi xảy ra khi:

- `init-db.sh` không chạy.
- `init-db.sh` chạy lỗi.
- Volume đã được khởi tạo dở.
- Volume được tạo từ phiên bản cấu hình cũ.

Sau khi sửa nguyên nhân, reset volume:

```powershell
docker compose down -v
docker compose up --build -d
```

### `/bin/sh^M: bad interpreter`

`scripts/init-db.sh` đang dùng CRLF.

Đổi file sang LF, sau đó reset volume vì lần khởi tạo database đã thất bại giữa chừng:

```powershell
docker compose down -v
docker compose up --build -d
```

### `SEED_EMAIL is required`

Nguyên nhân có thể là:

- `.env` chưa có `SEED_EMAIL`.
- `.env` chưa có `SEED_PASSWORD`.
- `.env` chưa có `SEED_ROLE`.
- Lệnh được chạy ngoài thư mục chứa `compose.yaml`.

Kiểm tra `.env`, sau đó chạy:

```powershell
docker compose --profile tools run --build --rm seed
```

### `Set ADMIN_EMAIL and ADMIN_PASSWORD ...`

Đảm bảo `.env` có:

```env
ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD=your-local-password
```

Đồng thời `scripts/load-env.ps1` phải có hai tên này trong `$walletNames`.

### `invalid_credentials` ở cuối demo

Nếu demo đã tạo thành công hai customer nhưng lỗi sau đó, thường là bước đăng nhập admin.

Nguyên nhân:

- Chưa chạy seed.
- Email admin trong DB khác `ADMIN_EMAIL`.
- Password lúc seed khác `ADMIN_PASSWORD`.
- Đã đổi password trong `.env`, nhưng seed không cập nhật password user cũ.

Dùng đúng password của lần seed đầu hoặc seed admin bằng email mới.

### Seed vẫn chạy code cũ

`docker compose up --build` không build service `seed` vì nó nằm trong profile `tools`.

Chạy:

```powershell
docker compose --profile tools run --build --rm seed
```

### Port bị chiếm

Kiểm tra port:

```powershell
Get-NetTCPConnection -LocalPort 8080,5436 -ErrorAction SilentlyContinue
```

- `8080`: API.
- `5436`: PostgreSQL từ máy host.

---

## 12. Các lệnh chẩn đoán nhanh

```powershell
docker compose ps -a
docker compose logs --no-color --tail 200 db
docker compose logs --no-color --tail 200 migrate
docker compose logs --no-color --tail 200 api
docker compose images
```