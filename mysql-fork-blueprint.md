# Blueprint Teknis: Fork PocketBase dengan Backend MySQL

Dokumen ini adalah panduan teknis untuk membuat proyek baru berbasis fork PocketBase yang memakai MySQL sebagai backend database utama. Ini bukan panduan untuk memodifikasi `pocketbase-libsql` secara langsung. Project `pocketbase-libsql` bekerja karena libSQL masih kompatibel dengan SQLite, sedangkan MySQL membutuhkan perubahan dialect dan perilaku database yang jauh lebih besar.

## 1. Ringkasan Keputusan

### Rekomendasi utama

Buat fork langsung dari repository upstream PocketBase, lalu jaga perubahan MySQL sebagai patch set kecil dan terstruktur. Jangan mulai dari fork libSQL ini, karena pendekatannya bergantung pada trik berikut:

- PocketBase tetap memakai asumsi SQLite.
- Driver libSQL didaftarkan sebagai builder SQLite-compatible.
- Banyak query, schema, migration, dan introspection PocketBase tetap berjalan karena libSQL dekat dengan SQLite.

Untuk MySQL, strategi yang benar adalah membuat lapisan database PocketBase benar-benar sadar dialect.

### Target realistis

Ada dua target yang harus dibedakan sejak awal:

1. **PoC MySQL adapter**
   - Tujuan: PocketBase bisa start, connect ke MySQL, membuat beberapa tabel dasar, dan menjalankan operasi sederhana.
   - Cocok untuk validasi awal.
   - Belum layak production.

2. **PocketBase MySQL-compatible fork**
   - Tujuan: fitur utama PocketBase bekerja dengan MySQL: collections, auth, files metadata, migrations, views, indexes, rules, realtime, backups, dan admin UI.
   - Membutuhkan patch PocketBase core.
   - Membutuhkan test matrix besar.
   - Ini adalah target sebenarnya jika ingin dipakai serius.

### Prinsip desain

- Jangan menyebar `if mysql` di seluruh codebase tanpa struktur.
- Buat abstraction layer untuk dialect database.
- Semua perubahan harus mudah di-rebase saat upstream PocketBase update.
- Mulai dari test baseline upstream sebelum mengubah apapun.
- Jangan mengejar semua fitur sekaligus; pecah menjadi fase yang bisa diverifikasi.


## 2. Bukti Resmi yang Sudah Diverifikasi

Bagian ini merangkum dasar faktual dari upstream PocketBase dan dependency resminya. Gunakan ini sebagai acuan sebelum mulai fork.

### 2.1 PocketBase resmi adalah SQLite-first

Dokumentasi resmi PocketBase memperkenalkan PocketBase sebagai backend dengan embedded database SQLite. Dokumentasi Collections juga menyebut collections backed by plain SQLite tables. Dokumentasi Go extension menyebut `DBConnect` untuk **custom SQLite driver/build**, misalnya mengganti driver SQLite bawaan dengan `mattn/go-sqlite3` atau `ncruces/go-sqlite3`. Ini penting: `DBConnect` memang extension point resmi, tetapi dokumentasi resminya bukan kontrak multi-database umum.

Implikasi untuk fork MySQL:

- Jangan menganggap `DBConnect` saja cukup.
- Rencana fork harus mengubah layer schema/query yang SQLite-specific.
- Dokumentasi fork harus jujur bahwa ini divergensi dari model resmi PocketBase.

### 2.2 `DBConnect` dipanggil untuk main dan auxiliary database

Di source upstream PocketBase, `DBConnectFunc` didefinisikan sebagai:

```go
type DBConnectFunc func(dbPath string) (*dbx.DB, error)
```

Dokumentasi resmi menjelaskan fungsi ini dipanggil dua kali: untuk `pb_data/data.db` sebagai database utama dan untuk auxiliary database yang dipakai logs/meta ephemeral.

Implikasi untuk fork MySQL:

- PoC harus memutuskan apakah `auxiliary.db` tetap SQLite atau ikut MySQL.
- Jika hanya main DB ke MySQL, path auxiliary harus fallback eksplisit ke default SQLite.
- Jika semua DB ke MySQL, fitur logs/meta juga harus ikut diaudit.

### 2.3 Core PocketBase memakai SQL SQLite secara langsung

Verifikasi terhadap source PocketBase v0.37.5 menunjukkan referensi SQLite langsung di core, antara lain:

- `core/db_table.go` memakai `PRAGMA_TABLE_INFO` untuk membaca kolom dan table info.
- `core/db_table.go` memakai `sqlite_master` dan `sqlite_schema` untuk index/table/view detection.
- `core/collection_record_table_sync.go` menjalankan `PRAGMA optimize`.
- `core/collection_record_table_sync.go` memakai `json_extract` untuk beberapa transformasi field.

Implikasi untuk fork MySQL:

- Audit harus dimulai dari `core/db_table.go` dan `core/collection_record_table_sync.go`.
- MySQL harus mengganti introspection ke `information_schema` atau abstraction lain.
- JSON/date/index behavior tidak cukup ditangani oleh query builder saja.

### 2.4 `dbx` support MySQL, tetapi PocketBase core tetap SQLite-oriented

Dependency `github.com/pocketbase/dbx` memiliki `BuilderFuncMap` untuk beberapa database, termasuk `mysql`, `postgres`, `mssql`, dan lainnya. Ini berarti query builder punya kemampuan menghasilkan SQL MySQL untuk sebagian operasi.

Namun ini tidak otomatis membuat PocketBase mendukung MySQL, karena PocketBase core tetap memiliki SQL raw dan schema assumptions yang SQLite-specific.

Implikasi untuk fork MySQL:

- `dbx.Open("mysql", dsn)` berguna untuk PoC koneksi.
- Setelah koneksi berhasil, blocker utama kemungkinan pindah ke schema sync, table introspection, indexes, views, migrations, dan query raw SQLite.
- Blueprint harus memperlakukan `dbx` MySQL support sebagai fondasi parsial, bukan solusi final.

### 2.5 Sumber verifikasi

Sumber yang dipakai untuk memverifikasi blueprint ini:

- Dokumentasi resmi PocketBase Introduction: menyebut PocketBase sebagai backend dengan embedded database SQLite.
- Dokumentasi resmi PocketBase Collections: menyebut collections backed by plain SQLite tables.
- Dokumentasi resmi PocketBase Go Overview bagian custom SQLite driver: menjelaskan `DBConnect` untuk custom SQLite driver/build dan menyebut pemanggilan untuk `data.db` serta auxiliary database.
- Source upstream PocketBase `core/base.go`: definisi `DBConnectFunc` dan konfigurasi `BaseAppConfig.DBConnect`.
- Source upstream PocketBase `core/db_table.go`: pemakaian `PRAGMA_TABLE_INFO`, `sqlite_master`, dan `sqlite_schema`.
- Source upstream PocketBase `core/collection_record_table_sync.go`: schema sync record table, `PRAGMA optimize`, dan `json_extract`.
- Source `github.com/pocketbase/dbx` `db.go` dan `builder_mysql.go`: `BuilderFuncMap` memang mencantumkan MySQL builder.

## 3. Persiapan Proyek Baru

### 3.1 Buat fork dari upstream PocketBase

Gunakan repository upstream PocketBase sebagai sumber utama:

```bash
git clone git@github.com:<org-anda>/pocketbase-mysql.git
cd pocketbase-mysql
git remote add upstream https://github.com/pocketbase/pocketbase.git
git fetch upstream
```

Struktur remote yang disarankan:

```text
origin      = fork milik Anda / organisasi Anda
upstream    = repository resmi PocketBase
```

Branch yang disarankan:

```text
main                 mirror stabil fork Anda
upstream/main        tracking branch upstream PocketBase
mysql/main           branch integrasi MySQL
mysql/poc-driver     PoC koneksi dan boot awal
mysql/dialect        abstraction dialect
mysql/schema         migration dan schema work
mysql/tests          hardening test matrix
```

Jika tim kecil, branch bisa disederhanakan:

```text
main
mysql
```

Namun tetap pisahkan commit berdasarkan area agar conflict upstream mudah dikelola.

### 3.2 Tooling minimum

Siapkan environment berikut:

- Go sesuai versi yang dipakai PocketBase upstream.
- Docker dan Docker Compose.
- MySQL 8.0.
- MariaDB opsional, hanya jika ingin kompatibilitas MariaDB.
- `golangci-lint` jika upstream menggunakannya atau Anda ingin CI tambahan.
- GitHub Actions atau CI lain.

Contoh `docker-compose.yml` untuk development:

```yaml
services:
  mysql:
    image: mysql:8.4
    environment:
      MYSQL_ROOT_PASSWORD: root
      MYSQL_DATABASE: pocketbase_test
      MYSQL_USER: pocketbase
      MYSQL_PASSWORD: pocketbase
    ports:
      - "3306:3306"
    command:
      - --character-set-server=utf8mb4
      - --collation-server=utf8mb4_0900_ai_ci
      - --default-time-zone=+00:00
    healthcheck:
      test: ["CMD", "mysqladmin", "ping", "-h", "127.0.0.1", "-uroot", "-proot"]
      interval: 5s
      timeout: 3s
      retries: 20
```

### 3.3 Konfigurasi environment target

Rancang konfigurasi eksplisit. Contoh:

```env
PB_DATABASE_DRIVER=mysql
PB_DATABASE_DSN=pocketbase:pocketbase@tcp(127.0.0.1:3306)/pocketbase_test?parseTime=true&charset=utf8mb4&collation=utf8mb4_0900_ai_ci&loc=UTC
```

Catatan:

- `parseTime=true` penting agar timestamp MySQL terbaca benar oleh Go.
- `loc=UTC` mengurangi bug timezone.
- Hindari `multiStatements=true` sebagai default. Aktifkan hanya jika migration benar-benar membutuhkan dan sudah diaudit dari sisi security; lebih aman memecah eksekusi statement di migration layer.
- Gunakan charset `utf8mb4`.
- Tentukan collation sejak awal agar behavior sorting/filter stabil.

## 4. Pemetaan Arsitektur PocketBase yang Perlu Diaudit

Sebelum implementasi, audit area berikut di upstream PocketBase.

### 4.1 Titik masuk koneksi database

PocketBase menyediakan konfigurasi koneksi database melalui `DBConnectFunc`. Secara resmi, dokumentasi PocketBase mencontohkannya untuk custom SQLite driver/build. Secara eksperimen, ini tetap titik masuk paling mudah untuk mencoba driver MySQL:

```go
type DBConnectFunc func(dbPath string) (*dbx.DB, error)
```

Namun ini hanya mengganti koneksi. Ini tidak mengubah asumsi SQLite di core PocketBase seperti PRAGMA, `sqlite_schema`, `sqlite_master`, dan beberapa fungsi JSON SQLite.

Yang perlu diperiksa:

- Default SQLite connection.
- Inisialisasi concurrent dan non-concurrent DB.
- Kapan PocketBase membuka main DB dan auxiliary DB.
- Apakah semua DB harus MySQL atau hanya main DB.

Rekomendasi awal:

- Untuk PoC, main DB diarahkan ke MySQL.
- Auxiliary/log/internal DB bisa tetap SQLite sementara jika tidak menghalangi.
- Untuk target penuh, tentukan apakah semua state harus pindah ke MySQL.

### 4.2 Query builder `dbx`

`github.com/pocketbase/dbx` sudah punya MySQL builder. Ini membantu untuk query biasa, tetapi tidak cukup untuk seluruh PocketBase.

Yang perlu dicek:

- Placeholder syntax.
- Identifier quoting.
- Limit/offset.
- Upsert behavior.
- Schema introspection.
- Column type mapping.

Jangan berasumsi semua query otomatis aman hanya karena `dbx` punya builder MySQL.

### 4.3 SQLite-specific code di PocketBase core

Cari dan audit pattern berikut di upstream PocketBase:

```bash
rg -n "sqlite|sqlite_schema|sqlite_master|PRAGMA|WITHOUT ROWID|json_extract|strftime|datetime\(|AUTOINCREMENT|ON CONFLICT|RETURNING" .
```

Area umum yang berisiko:

- Schema introspection via `sqlite_schema` atau `sqlite_master`.
- View definition parsing.
- Index creation dan detection.
- DDL untuk alter table.
- Migration generator.
- JSON functions.
- Date/time functions.
- Conflict handling.
- Transaction lock behavior.
- Foreign key behavior.

### 4.4 Collection schema dan migrations

PocketBase banyak bergantung pada kemampuan mengubah schema collection secara dinamis. Ini area tersulit untuk MySQL.

Audit operasi berikut:

- Create collection.
- Rename collection.
- Delete collection.
- Add field.
- Rename field.
- Change field type.
- Drop field.
- Create index.
- Drop index.
- Create view collection.
- Update view collection.

SQLite dan MySQL punya perbedaan besar pada DDL. MySQL juga sering melakukan implicit commit pada DDL, sehingga transaksi migration tidak selalu sama seperti SQLite.

### 4.5 View collections

PocketBase mendukung view collection. Di SQLite, introspection dan parsing view memakai mekanisme SQLite. Untuk MySQL, perlu strategi baru.

Pertanyaan desain:

- Apakah view collection tetap didukung di fase awal?
- Apakah definisi view disimpan sebagai metadata PocketBase, bukan dibaca balik dari `information_schema`?
- Bagaimana validasi kolom view dilakukan?
- Bagaimana migration view dilakukan saat SQL berubah?

Rekomendasi:

- PoC boleh menonaktifkan view collection dengan error eksplisit.
- Target production harus punya implementasi MySQL view yang lengkap.

### 4.6 Tipe data

Buat mapping tipe data eksplisit.

Contoh awal:

| PocketBase / SQLite concept | MySQL target |
| --- | --- |
| text | `TEXT` atau `VARCHAR(n)` |
| bool | `TINYINT(1)` |
| number integer | `BIGINT` |
| number float | `DOUBLE` atau `DECIMAL` sesuai kebutuhan |
| date | `DATETIME(3)` UTC |
| json/select multi | `JSON` atau `TEXT` berisi JSON |
| file metadata | `JSON` atau `TEXT` berisi JSON |
| relation id | `VARCHAR(15)` / sesuai ID generator PocketBase |
| created/updated | `DATETIME(3)` UTC |

Keputusan penting:

- Apakah memakai native `JSON` MySQL atau tetap `TEXT` JSON agar perilaku dekat SQLite?
- Apakah ID tetap string seperti PocketBase default?
- Apakah timestamp disimpan UTC tanpa timezone?
- Apakah decimal precision dibutuhkan?

Rekomendasi awal:

- Pertahankan ID string PocketBase.
- Simpan timestamp UTC di `DATETIME(3)`.
- Untuk kompatibilitas awal, simpan JSON sebagai `TEXT` dulu, lalu optimasi ke native `JSON` belakangan jika perlu.

## 5. Strategi Implementasi Bertahap

### Fase 0: Baseline upstream

Tujuan: pastikan fork bersih sebelum perubahan MySQL.

Langkah:

1. Clone upstream PocketBase.
2. Checkout tag atau commit target.
3. Jalankan seluruh test upstream.
4. Catat test yang gagal karena environment lokal, jika ada.
5. Buat tag internal:

```bash
git tag baseline-pocketbase-vX.Y.Z
```

Output fase ini:

- Fork bisa build tanpa perubahan.
- Test baseline terdokumentasi.
- Tidak ada patch MySQL dulu.

### Fase 1: PoC driver MySQL minimal

Tujuan: PocketBase bisa membuka koneksi MySQL.

Langkah:

1. Tambah dependency MySQL driver:

```go
_ "github.com/go-sql-driver/mysql"
```

2. Tambah config database driver dan DSN.
3. Buat `DBConnectFunc` untuk MySQL:

```go
func mysqlDBConnect(dsn string) core.DBConnectFunc {
    return func(dbPath string) (*dbx.DB, error) {
        return dbx.Open("mysql", dsn)
    }
}
```

4. Jalankan binary dengan `PB_DATABASE_DRIVER=mysql`.
5. Catat error pertama dari PocketBase core.

Output fase ini:

- Daftar error nyata dari startup.
- Bukti apakah kegagalan ada di migration, schema introspection, atau query awal.
- Tidak perlu langsung memperbaiki semua error.

Kriteria sukses:

- App minimal bisa connect ke MySQL.
- Error berikutnya sudah berasal dari asumsi schema/dialect, bukan koneksi.

### Fase 2: Database dialect abstraction

Tujuan: menyiapkan tempat resmi untuk perbedaan SQLite dan MySQL.

Buat interface internal semacam:

```go
type Dialect interface {
    Name() string
    QuoteIdent(name string) string
    Placeholder(index int) string
    ColumnType(field FieldLike) string
    CurrentTimestampSQL() string
    TableExistsSQL(table string) QuerySpec
    ColumnExistsSQL(table string, column string) QuerySpec
    ListIndexesSQL(table string) QuerySpec
    CreateIndexSQL(index IndexSpec) string
    DropIndexSQL(table string, index string) string
}
```

Nama dan bentuk final harus mengikuti struktur PocketBase, tetapi prinsipnya:

- Semua SQL khusus dialect masuk ke satu area.
- SQLite tetap menjadi default behavior.
- MySQL menambahkan implementasi, bukan mengganti semua kode.

Output fase ini:

- `sqliteDialect` merepresentasikan behavior lama.
- `mysqlDialect` mulai dipakai untuk path MySQL.
- Test unit untuk SQL generation.

Kriteria sukses:

- Patch SQLite minimal.
- Behavior SQLite upstream tetap lulus test.
- MySQL punya tempat implementasi yang jelas.

### Fase 3: Schema dan migration layer

Tujuan: operasi collection schema bekerja di MySQL.

Prioritas implementasi:

1. Create base system tables.
2. Create normal collection table.
3. Add field.
4. Drop field.
5. Rename field.
6. Change field type.
7. Create/drop index.
8. Relation field.
9. Auth collection.
10. View collection.

Untuk MySQL, audit SQL DDL:

- `CREATE TABLE`.
- `ALTER TABLE ADD COLUMN`.
- `ALTER TABLE DROP COLUMN`.
- `ALTER TABLE RENAME COLUMN`.
- `ALTER TABLE MODIFY COLUMN`.
- `CREATE INDEX`.
- `DROP INDEX`.
- `CREATE VIEW`.
- `DROP VIEW`.

Perhatikan:

- MySQL DDL sering implicit commit.
- Beberapa perubahan kolom membutuhkan full column definition.
- Nama index unik per table, bukan global seperti beberapa asumsi lain.
- Identifier length limit MySQL adalah 64 karakter.
- Reserved words MySQL berbeda dari SQLite.

Output fase ini:

- Admin UI bisa membuat collection dan field dasar.
- Migration JS/Go bisa membuat schema di MySQL.
- Tests untuk schema operations utama.

### Fase 4: Query compatibility

Tujuan: operasi runtime PocketBase bekerja.

Area yang harus dites:

- CRUD record.
- Filter rules.
- Sort.
- Pagination.
- Expand relation.
- Auth password login.
- Auth token refresh.
- File metadata.
- Realtime subscriptions.
- Admin API.
- Batch requests.
- Import/export jika didukung.

Perbedaan MySQL yang harus diperhatikan:

- Case sensitivity tergantung collation.
- Boolean adalah numeric.
- Empty string vs NULL.
- JSON comparison berbeda.
- Date parsing berbeda.
- `LIKE` behavior tergantung collation.
- Transaction isolation default biasanya `REPEATABLE READ`.
- Locking dan deadlock behavior berbeda dari SQLite.

Output fase ini:

- Test suite MySQL-specific.
- Dokumentasi perbedaan behavior yang tidak bisa disamakan.
- Manual QA checklist.

### Fase 5: Hardening production

Tujuan: fork layak dipakai oleh user nyata.

Yang perlu ditambahkan:

- Connection pool tuning:

```go
sqlDB.SetMaxOpenConns(n)
sqlDB.SetMaxIdleConns(n)
sqlDB.SetConnMaxLifetime(duration)
```

- Migration lock agar multiple instance tidak menjalankan migration bersamaan.
- Health check database.
- Backup/restore story.
- Observability untuk slow query.
- Dokumentasi konfigurasi MySQL.
- Upgrade guide.

Pertanyaan penting:

- Apakah PocketBase MySQL fork mendukung multi-instance?
- Bagaimana realtime bekerja di multi-instance?
- Apakah file storage tetap lokal atau harus object storage?
- Bagaimana backup dilakukan: `mysqldump`, physical backup, atau custom export?

## 6. Testing Strategy

### 6.1 Test matrix database

Minimum:

```text
MySQL 8.0 latest
MySQL 8.4 LTS
```

Opsional:

```text
MariaDB 10.11 LTS
MariaDB latest
```

Jangan klaim MariaDB support jika belum dites, karena dialect dan JSON behavior berbeda.

### 6.2 CI matrix

Contoh matrix:

```yaml
strategy:
  matrix:
    db:
      - mysql:8.0
      - mysql:8.4
```

Setiap run:

1. Start MySQL service.
2. Wait healthcheck.
3. Run Go tests SQLite/default.
4. Run Go tests MySQL dengan env `PB_DATABASE_DRIVER=mysql`.
5. Run integration tests admin/API.

### 6.3 Kategori test wajib

- **Dialect unit tests**: SQL generation per dialect.
- **Schema integration tests**: create/update/delete collection dan fields.
- **Record API tests**: CRUD dan filtering.
- **Auth tests**: register/login/refresh/permissions.
- **Migration tests**: apply, rollback jika ada, idempotency.
- **View tests**: jika view collection didukung.
- **Concurrency tests**: parallel writes, migration lock, transactions.
- **Upgrade tests**: database dari versi fork lama ke versi baru.

### 6.4 Manual QA gate

Setiap milestone harus diuji melalui surface nyata:

1. Jalankan server.
2. Buka admin UI.
3. Buat admin pertama.
4. Buat collection baru.
5. Tambah beberapa field.
6. Buat record.
7. Edit record.
8. Buat auth collection.
9. Register/login user.
10. Upload file jika fitur file sudah ditargetkan.
11. Restart server dan pastikan data tetap ada.
12. Jalankan migration dan pastikan schema berubah benar di MySQL.

## 7. Strategi Mengikuti Update PocketBase Upstream

### 7.1 Model branch

Gunakan model patch stack:

```text
upstream/main
    ↓ merge/rebase berkala
mysql/main
    ├── patch: config driver
    ├── patch: dialect abstraction
    ├── patch: mysql schema implementation
    ├── patch: mysql tests
    └── patch: docs/release
```

Tujuannya agar perubahan MySQL tidak menjadi satu commit besar yang sulit di-rebase.

### 7.2 Jadwal sync upstream

Rekomendasi:

- Sync kecil mingguan dari `upstream/main` jika aktif development.
- Sync wajib setiap upstream release/tag baru.
- Jangan menunggu terlalu lama, karena conflict akan membesar.

Workflow:

```bash
git fetch upstream
git checkout mysql/main
git merge upstream/main
# atau rebase jika tim sepakat memakai rebase workflow
go test ./...
```

Jika menggunakan rebase:

```bash
git fetch upstream
git checkout mysql/main
git rebase upstream/main
```

Untuk tim, merge sering lebih aman karena tidak rewrite history. Rebase cocok jika branch belum dipublish atau tim sangat disiplin.

### 7.3 Tracking upstream changes

Saat upstream update, audit area berikut:

```bash
rg -n "sqlite|sqlite_schema|sqlite_master|PRAGMA|migrate|migration|collection|view|index|dbx|DBConnect" .
```

Buat checklist per upstream release:

- [ ] Apakah PocketBase mengubah schema collection?
- [ ] Apakah ada migration baru?
- [ ] Apakah ada query SQLite-specific baru?
- [ ] Apakah `dbx` version berubah?
- [ ] Apakah admin UI mengasumsikan behavior schema tertentu?
- [ ] Apakah backup/export/import berubah?
- [ ] Apakah auth/session behavior berubah?
- [ ] Apakah tests upstream baru perlu dibuat versi MySQL?

### 7.4 Conflict playbook

Saat conflict:

1. Jangan langsung pilih versi fork.
2. Baca perubahan upstream dulu.
3. Identifikasi apakah upstream mengubah behavior domain atau hanya refactor.
4. Pertahankan behavior upstream untuk SQLite.
5. Port perubahan baru ke MySQL dialect.
6. Tambahkan/regenerasi test jika upstream menambah scenario baru.
7. Jalankan SQLite tests dan MySQL tests.

Aturan penting:

- SQLite behavior upstream tidak boleh rusak.
- MySQL patch tidak boleh menurunkan fitur upstream tanpa dokumentasi eksplisit.
- Jika upstream menambah fitur database baru, MySQL harus memilih: implement, disable eksplisit, atau tandai unsupported.

### 7.5 Changelog internal

Buat file khusus, misalnya:

```text
docs/mysql-upstream-sync-log.md
```

Isi per sync:

```markdown
## Sync upstream vX.Y.Z - YYYY-MM-DD

Base: upstream tag vX.Y.Z
Fork branch: mysql/main

### Upstream database-related changes
- ...

### Conflicts resolved
- ...

### MySQL changes needed
- ...

### Tests run
- ...

### Known gaps
- ...
```

Ini penting agar maintainer baru tidak perlu menebak alasan patch lama.

## 8. Packaging dan Release

### 8.1 Nama binary dan module

Jangan memakai nama yang membingungkan dengan PocketBase resmi.

Contoh:

```text
pocketbase-mysql
```

Atau:

```text
pb-mysql
```

Dokumentasi harus jelas:

- Ini fork tidak resmi.
- Versi PocketBase upstream yang menjadi base.
- Fitur yang belum kompatibel.
- MySQL version yang didukung.

### 8.2 Versioning

Gunakan versi yang mencantumkan upstream base:

```text
v0.37.5-mysql.1
v0.37.5-mysql.2
v0.38.0-mysql.1
```

Format ini memudahkan user tahu fork berbasis PocketBase versi berapa.

### 8.3 Release checklist

Sebelum release:

- [ ] Merge/sync upstream tag target.
- [ ] Run full SQLite/default test suite.
- [ ] Run full MySQL test suite.
- [ ] Manual QA admin UI.
- [ ] Manual QA migration.
- [ ] Update compatibility matrix.
- [ ] Update known limitations.
- [ ] Build binaries.
- [ ] Publish Docker image jika ada.
- [ ] Tag release dengan format fork.

## 9. Risiko Utama

### 9.1 Scope creep

Risiko terbesar adalah mengira ini hanya penggantian driver. Batas aman:

- PoC boleh kecil.
- Production fork harus menganggap database layer sebagai proyek besar.

### 9.2 Divergence dari upstream

Semakin banyak patch tersebar, semakin sulit update. Mitigasi:

- Patch kecil.
- Dialect abstraction.
- Sync upstream rutin.
- Test SQLite tetap jalan.

### 9.3 Behavior MySQL tidak sama dengan SQLite

Contoh:

- Sorting dan case sensitivity.
- Transaction isolation.
- JSON behavior.
- DDL transactions.
- Foreign key enforcement.
- Timezone.

Mitigasi:

- Dokumentasikan perbedaan.
- Jangan klaim kompatibilitas penuh sebelum test lengkap.
- Buat compatibility suite untuk fitur PocketBase utama.

### 9.4 Multi-instance expectations

User mungkin mengira MySQL otomatis membuat PocketBase aman untuk multi-instance. Belum tentu.

Perlu audit:

- Realtime event delivery.
- Migration locking.
- File storage lokal.
- Scheduled jobs.
- Cache in-memory.

Dokumentasi harus eksplisit apakah fork mendukung single-instance saja atau multi-instance.

## 10. Milestone Roadmap yang Disarankan

### Milestone 1: Research baseline

Deliverable:

- Fork upstream.
- Test baseline.
- Audit SQLite-specific code.
- Dokumen gap analysis.

Exit criteria:

- Tim tahu daftar area yang harus diubah.

### Milestone 2: MySQL boot PoC

Deliverable:

- Config `PB_DATABASE_DRIVER=mysql` dan `PB_DATABASE_DSN`.
- Koneksi MySQL berhasil.
- Error startup berikutnya terdokumentasi.

Exit criteria:

- Tidak ada blocker di driver/DSN.

### Milestone 3: Schema minimal

Deliverable:

- System tables bisa dibuat.
- Base collection bisa dibuat.
- CRUD record sederhana berjalan.

Exit criteria:

- Admin UI bisa membuat collection sederhana dan record tersimpan di MySQL.

### Milestone 4: Feature compatibility alpha

Deliverable:

- Auth collections.
- Relations.
- Indexes.
- Filters/sorts/expands.
- Migration command.

Exit criteria:

- Aplikasi kecil bisa dibangun memakai fork ini.

### Milestone 5: Production beta

Deliverable:

- Full CI matrix.
- Manual QA checklist lulus.
- Upgrade tests.
- Known limitations jelas.
- Release artifacts.

Exit criteria:

- Bisa dipakai oleh early adopter dengan batasan jelas.

## 11. Go / No-Go Criteria

Lanjutkan proyek jika:

- PoC bisa boot tanpa patch besar yang merusak core.
- Schema abstraction bisa dibuat tanpa menyentuh terlalu banyak area unrelated.
- Upstream test SQLite tetap bisa dipertahankan.
- Tim siap maintain fork jangka panjang.

Hentikan atau ubah strategi jika:

- Terlalu banyak query internal PocketBase harus di-fork manual.
- View/migration layer membutuhkan rewrite besar tanpa test memadai.
- Sync upstream menjadi terlalu mahal sejak awal.
- Target sebenarnya hanya butuh integrasi data dengan MySQL, bukan mengganti database internal PocketBase.

Alternatif jika go/no-go gagal:

- Tetap pakai PocketBase SQLite/libSQL sebagai primary.
- Sinkronkan data tertentu ke MySQL via hooks.
- Buat service terpisah yang membaca PocketBase API dan menulis ke MySQL.
- Gunakan MySQL hanya untuk domain data aplikasi, sementara PocketBase tetap mengelola auth/admin/files.

## 12. Checklist Awal untuk Proyek Baru

Gunakan checklist ini saat mulai repository baru:

- [ ] Fork upstream PocketBase, bukan fork libSQL.
- [ ] Tambah remote `upstream`.
- [ ] Pilih upstream tag pertama sebagai base.
- [ ] Jalankan test upstream tanpa modifikasi.
- [ ] Buat Docker Compose MySQL.
- [ ] Buat branch `mysql/main`.
- [ ] Audit SQLite-specific code.
- [ ] Tulis gap analysis.
- [ ] Tambah MySQL driver dan config minimal.
- [ ] Coba boot dengan MySQL.
- [ ] Catat error startup pertama.
- [ ] Desain dialect abstraction.
- [ ] Implement schema minimal.
- [ ] Tambah MySQL integration tests.
- [ ] Buat upstream sync log.
- [ ] Tentukan compatibility matrix.
- [ ] Tentukan release/versioning policy.

## 13. Kesimpulan

Membuat PocketBase dengan backend MySQL bisa dilakukan, tetapi harus diperlakukan sebagai fork database-layer PocketBase, bukan sekadar mengganti koneksi. Jalur paling aman adalah:

1. Fork langsung dari upstream PocketBase.
2. Pertahankan SQLite behavior upstream tetap utuh.
3. Tambahkan MySQL melalui abstraction layer yang jelas.
4. Uji setiap fitur PocketBase utama terhadap MySQL.
5. Sync upstream secara rutin dengan patch stack kecil, dan baca changelog karena PocketBase belum menjanjikan full backward compatibility sebelum v1.0.0.

Jika target hanya integrasi dengan MySQL, sinkronisasi via hooks/API jauh lebih murah. Jika target adalah PocketBase MySQL penuh, siapkan maintenance jangka panjang sebagai bagian inti proyek.
