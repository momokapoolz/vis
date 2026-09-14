# Digital Wallet & Double-Entry Ledger Engine --- PoC Setup

## 1. Project Overview

Mục tiêu của PoC là xây dựng backend ví điện tử giả lập. Không xử lý
tiền thật.

Điểm cốt lõi:

-   User có thể sở hữu một hoặc nhiều wallet.
-   Mọi biến động tiền được ghi nhận bằng ledger.
-   Không cập nhật `wallet.balance` trực tiếp.
-   Mỗi transaction tài chính phải có debit và credit tương ứng.
-   Các ledger entries của một transaction phải được ghi trong cùng một DB transaction.
-   Chống request lặp bằng `Idempotency-Key`.
-   Chống overspending khi có concurrent transfers.
-   Hỗ trợ mock payment provider webhook với HMAC.
-   Admin có thể refund/reverse transaction bằng reversing entries.
-   Có reconciliation job kiểm tra ledger.

------------------------------------------------------------------------

## 2. Core Principle

Không thiết kế theo kiểu:

``` text
wallet.balance = wallet.balance - amount
```

Thay vào đó:

``` text
Transaction T001
├── Debit  Wallet A: 100,000
└── Credit Wallet B: 100,000
```

Balance được tính từ ledger:

``` text
Balance = Total Credits - Total Debits
```

> Quy ước debit/credit phải được chốt trước khi implement và sử dụng
> nhất quán trong toàn bộ project.

------------------------------------------------------------------------

## 3. MVP Scope

### Authentication

-   Register
-   Login
-   JWT access token
-   Refresh token

### RBAC

Ba role:

-   `customer`
-   `support`
-   `admin`

Quyền:

  -----------------------------------------------------------------------
  Role                                Permission
  ----------------------------------- -----------------------------------
  Customer                            Quản lý wallet của mình, xem
                                      balance, transfer, xem transaction

  Support                             Tra cứu user/wallet/transaction

  Admin                               Quyền Support + reversal/refund +
                                      reconciliation
  -----------------------------------------------------------------------

### Wallet

-   Một user có thể có nhiều wallet.
-   MVP nên chỉ hỗ trợ một currency, ví dụ `VND`.
-   Không lưu balance trực tiếp trong wallet.

### Money Movement

MVP cần hỗ trợ:

-   Mock deposit
-   Wallet-to-wallet transfer
-   Reversal/refund

------------------------------------------------------------------------

## 4. Business Rules

Các rule nên chốt trước khi code:

1.  `amount > 0`.
2.  MVP chỉ hỗ trợ một currency.
3.  Không cho transfer nếu source wallet không đủ balance.
4.  Không cho transfer từ wallet sang chính nó.
5.  Customer chỉ được transfer từ wallet thuộc sở hữu của mình.
6.  Ledger entries sau khi tạo không được sửa hoặc xóa.
7.  Refund/reversal phải tạo transaction mới.
8.  Mỗi financial transaction phải cân bằng debit và credit.
9.  Ledger entries của cùng transaction phải commit atomically.
10. Mọi mutating endpoint phải có `Idempotency-Key`.
11. Webhook phải verify HMAC trước khi xử lý.
12. Một webhook event không được process nhiều lần.

------------------------------------------------------------------------

## 5. Initial Database Design

### users

``` text
id
email
password_digest
role
created_at
updated_at
```

### refresh_tokens

``` text
id
user_id
token_digest
expires_at
revoked_at
created_at
```

### wallets

``` text
id
user_id
currency
status
created_at
updated_at
```

Không thêm `balance` ở phiên bản đầu tiên.

### transactions

``` text
id
transaction_type
status
reference_id
reversed_transaction_id
created_at
```

`transaction_type` có thể gồm:

``` text
deposit
transfer
reversal
```

### ledger_entries

``` text
id
transaction_id
wallet_id
entry_type
amount
created_at
```

`entry_type`:

``` text
debit
credit
```

### idempotency_keys

``` text
id
user_id
key
request_hash
status
response_code
response_body
created_at
```

Nên có unique constraint phù hợp, ví dụ:

``` text
UNIQUE(user_id, key)
```

### webhook_events

``` text
id
provider_event_id
payload
status
processed_at
created_at
```

`provider_event_id` nên unique để chống webhook duplicate.

------------------------------------------------------------------------

## 6. Ledger Engine

Ledger Engine là core service của project.

Controller không nên tự tạo ledger entries.

Concept:

``` text
LedgerService.post_transaction(
    debit_account,
    credit_account,
    amount,
    transaction_type
)
```

Service phải đảm bảo:

``` text
amount > 0
debit amount == credit amount
transaction được tạo
debit entry được tạo
credit entry được tạo
```

Toàn bộ operation phải nằm trong:

``` text
BEGIN
    create transaction
    create debit entry
    create credit entry
COMMIT
```

Nếu bất kỳ bước nào lỗi:

``` text
ROLLBACK
```

Không được tồn tại transaction chỉ có một phía.

------------------------------------------------------------------------

## 7. Balance Calculation

Endpoint dự kiến:

``` http
GET /wallets/:id/balance
```

Balance không lấy từ column `wallet.balance`.

Ví dụ:

``` text
Credits = 1,000,000
Debits  =   300,000

Balance = 700,000
```

Logic:

``` text
Balance = SUM(Credit Entries) - SUM(Debit Entries)
```

------------------------------------------------------------------------

## 8. Initial Funding / Mock Deposit

Wallet mới:

``` text
Balance = 0
```

Cần cơ chế giả lập nạp tiền để test transfer.

Không nên tạo một ledger entry đơn lẻ.

Có thể tạo internal/system wallet:

``` text
Provider Clearing
```

Ví dụ deposit 1,000,000:

``` text
Transaction D001

Provider Clearing
Debit 1,000,000

Customer Wallet A
Credit 1,000,000
```

Sau đó:

``` text
Wallet A balance = 1,000,000
```

------------------------------------------------------------------------

## 9. Transfer Flow

Endpoint dự kiến:

``` http
POST /transfers
Idempotency-Key: <unique-key>
Authorization: Bearer <JWT>
```

Payload:

``` json
{
  "source_wallet_id": 1,
  "destination_wallet_id": 2,
  "amount": 100000
}
```

Flow:

``` text
Receive request
      ↓
Authenticate
      ↓
Authorize
      ↓
Validate Idempotency-Key
      ↓
Validate payload
      ↓
Lock relevant wallet/account rows
      ↓
Calculate source balance
      ↓
Enough balance?
   ├── NO → Reject
   └── YES
          ↓
       BEGIN DB TRANSACTION
          ↓
       Create Transaction
          ↓
       Debit Source Wallet
          ↓
       Credit Destination Wallet
          ↓
       COMMIT
          ↓
       Save idempotency result
          ↓
       Return response
```

------------------------------------------------------------------------

## 10. Concurrency Protection

Case cần xử lý:

``` text
Wallet A = 100,000

Request 1:
A → B = 80,000

Request 2:
A → C = 80,000
```

Nếu cả hai request đọc balance cùng lúc, cả hai có thể cùng nghĩ A đủ
tiền.

Kết quả đúng:

``` text
Request 1 → SUCCESS
Request 2 → INSUFFICIENT FUNDS
```

Hướng PoC:

``` text
SELECT ... FOR UPDATE
```

hoặc row-level locking tương đương của ORM/framework.

Lock phải được giữ trong DB transaction thích hợp.

------------------------------------------------------------------------

## 11. Idempotency

Mọi mutating endpoint yêu cầu:

``` http
Idempotency-Key: abc-123
```

Request đầu tiên:

``` text
abc-123
A → B 100,000
```

Backend:

``` text
execute
save transaction/result
return response
```

Request thứ hai với cùng key và cùng payload:

``` text
Không execute lại.
Trả lại result của request trước.
```

Nếu:

``` text
same key
different payload
```

nên reject, ví dụ:

``` http
409 Conflict
```

Nên lưu hash của request để phát hiện trường hợp này.

------------------------------------------------------------------------

## 12. Authentication

API dự kiến:

``` http
POST /auth/register
POST /auth/login
POST /auth/refresh
```

Login trả:

``` json
{
  "access_token": "...",
  "refresh_token": "..."
}
```

Access token dùng để gọi protected APIs.

Refresh token dùng để lấy access token mới.

------------------------------------------------------------------------

## 13. Wallet APIs

API cơ bản:

``` http
POST /wallets
GET  /wallets
GET  /wallets/:id
GET  /wallets/:id/balance
```

Customer chỉ được truy cập wallet phù hợp với quyền của mình.

------------------------------------------------------------------------

## 14. Transaction APIs

``` http
POST /transfers
GET  /transactions/:id
GET  /wallets/:id/transactions
```

Admin:

``` http
POST /admin/transactions/:id/reverse
```

------------------------------------------------------------------------

## 15. Mock Payment Provider Webhook

Endpoint:

``` http
POST /webhooks/mock-provider
```

Provider gửi:

``` json
{
  "event_id": "evt_001",
  "event": "deposit.success",
  "wallet_id": 1,
  "amount": 500000
}
```

Header:

``` http
X-Signature: <HMAC signature>
```

Flow:

``` text
Receive raw request body
        ↓
Calculate HMAC(secret, raw_body)
        ↓
Compare signature
        ↓
Invalid?
   ├── YES → Reject
   └── NO
        ↓
Check event_id
        ↓
Already processed?
   ├── YES → Return safely
   └── NO
        ↓
Create balanced ledger transaction
        ↓
Save webhook event
```

------------------------------------------------------------------------

## 16. Reversal / Refund

Không sửa:

``` text
Original Transaction
```

Không xóa:

``` text
Original Ledger Entries
```

Ví dụ transaction ban đầu:

``` text
T001

Wallet A
Debit 100,000

Wallet B
Credit 100,000
```

Reversal:

``` text
T002
reversed_transaction_id = T001

Wallet B
Debit 100,000

Wallet A
Credit 100,000
```

Audit history:

``` text
T001 = original transfer
T002 = reversal of T001
```

------------------------------------------------------------------------

## 17. Reconciliation

Nightly job kiểm tra integrity của ledger.

Invariant quan trọng:

``` text
Total Debit == Total Credit
```

Có thể kiểm tra theo từng transaction:

``` text
Transaction T001

Debit  = 100,000
Credit = 100,000

PASS
```

Nếu:

``` text
Debit  = 100,000
Credit = 90,000

FAIL
```

Job cần log/report các transaction bị lệch.

PoC chưa cần hệ thống alert phức tạp.

------------------------------------------------------------------------

## 18. Recommended Implementation Order

### Milestone 1 --- Foundation

-   Chọn tech stack.
-   Khởi tạo project.
-   Setup database.
-   Setup migrations.
-   Chốt accounting convention.
-   Tạo database schema.

### Milestone 2 --- Ledger Core

-   User.
-   Wallet.
-   Transaction.
-   Ledger Entry.
-   Ledger Service.
-   Balance calculation.
-   Mock initial funding.

Mục tiêu:

``` text
Có thể tạo balanced transaction và tính balance từ ledger.
```

### Milestone 3 --- Transfer

Implement:

``` http
POST /transfers
```

Bao gồm:

-   validation
-   ownership
-   insufficient funds
-   DB transaction
-   debit
-   credit

### Milestone 4 --- Concurrency

-   Row-level locking.
-   Concurrent transfer test.
-   Verify không thể overspend.

### Milestone 5 --- Idempotency

-   `Idempotency-Key`.
-   Request hash.
-   Cached/stored response.
-   Duplicate request handling.
-   Same key + different payload handling.

### Milestone 6 --- Authentication & RBAC

-   Register.
-   Login.
-   JWT.
-   Refresh token.
-   Customer.
-   Support.
-   Admin.

### Milestone 7 --- Payment Webhook

-   Mock provider.
-   HMAC signing.
-   HMAC verification.
-   Duplicate event protection.
-   Deposit ledger transaction.

### Milestone 8 --- Reversal

-   Admin reversal endpoint.
-   Reversing transaction.
-   Prevent invalid/double reversal.

### Milestone 9 --- Reconciliation

-   Reconciliation service.
-   Nightly scheduled job.
-   Detect unbalanced transactions.
-   Logging/report.

### Milestone 10 --- Finalization

-   Integration tests.
-   Concurrency tests.
-   Swagger/OpenAPI.
-   Seed/demo data.
-   README.
-   Architecture diagram.

------------------------------------------------------------------------

## 19. Critical Test Cases

### Ledger

``` text
Debit == Credit
```

for every successful financial transaction.

### Atomicity

Simulate failure after debit creation:

``` text
Debit created
Credit fails
```

Expected:

``` text
ROLLBACK
No ledger entries persisted
```

### Insufficient Balance

``` text
Balance = 100k
Transfer = 150k

Expected:
Rejected
```

### Concurrent Transfer

``` text
Balance = 100k

A → B 80k
A → C 80k
```

Expected:

``` text
Only one succeeds.
```

### Idempotency

Same key + same payload twice:

``` text
Only one financial transaction exists.
```

Same key + different payload:

``` text
Rejected.
```

### Webhook HMAC

Invalid signature:

``` text
Rejected.
No ledger mutation.
```

### Duplicate Webhook

Same `event_id` twice:

``` text
Only processed once.
```

### Reversal

After reversal:

``` text
Original entries remain unchanged.
Reversing entries exist.
Net financial effect = 0.
```

### Reconciliation

Normal data:

``` text
Debit == Credit → PASS
```

Corrupted/test anomaly:

``` text
Debit != Credit → FAIL/report
```

------------------------------------------------------------------------

## 20. Definition of Done for the PoC

PoC có thể xem là hoàn thành khi demo được toàn bộ flow:

``` text
1. Register/Login
        ↓
2. Create Wallet A + Wallet B
        ↓
3. Mock deposit Wallet A = 1,000,000
        ↓
4. Check balance from ledger
        ↓
5. Transfer A → B = 100,000
        ↓
6. Verify:
   A = 900,000
   B = 100,000
        ↓
7. Retry transfer với same Idempotency-Key
        ↓
8. Verify không bị transfer lần hai
        ↓
9. Test 2 concurrent transfers
        ↓
10. Verify không overspend
        ↓
11. Admin reverse transaction
        ↓
12. Verify reversing entries
        ↓
13. Run reconciliation
        ↓
14. Debit == Credit
```

------------------------------------------------------------------------

## 21. Development Priority

Nếu thời gian PoC hạn chế, ưu tiên theo thứ tự:

``` text
1. Ledger correctness
2. DB transaction / atomicity
3. Balance derived from ledger
4. Transfer
5. Concurrency protection
6. Idempotency
7. Authentication / RBAC
8. Webhook HMAC
9. Reversal
10. Reconciliation
11. Documentation / Swagger
```

Không nên dành quá nhiều thời gian cho UI, email, notification hoặc các
chức năng ngoài MVP.

------------------------------------------------------------------------

## 22. Main Technical Risks

Các phần cần review kỹ nhất:

-   Debit/credit convention không nhất quán.
-   Balance calculation sai.
-   Ledger entries được tạo ngoài DB transaction.
-   Race condition khi kiểm tra balance.
-   Idempotency check bị race condition.
-   Duplicate webhook.
-   Reversal làm thay đổi transaction cũ.
-   Dùng floating-point cho amount.

Với tiền, nên dùng integer theo đơn vị nhỏ nhất phù hợp với currency
hoặc fixed-precision decimal; không dùng `float/double` để tính tiền.

------------------------------------------------------------------------

## 23. First Tasks to Start

Trước khi viết controller/API, hoàn thành bốn việc:

``` text
[ ] Chọn tech stack
[ ] Vẽ ERD
[ ] Chốt debit/credit convention
[ ] Vẽ transfer sequence/flow
```

Sau đó:

``` text
[ ] Initialize repository
[ ] Configure database
[ ] Create migrations
[ ] Implement models
[ ] Implement Ledger Service
[ ] Write ledger tests
[ ] Implement balance
[ ] Implement transfer
```

Đây là điểm bắt đầu thực tế cho PoC.
