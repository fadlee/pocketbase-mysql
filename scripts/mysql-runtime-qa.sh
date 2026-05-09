#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

container_name="${MYSQL_QA_CONTAINER:-pb-mysql-runtime-qa}"
mysql_port="${MYSQL_QA_MYSQL_PORT:-3307}"
http_addr="${MYSQL_QA_HTTP_ADDR:-127.0.0.1:18090}"
tmp_dir="${MYSQL_QA_TMP_DIR:-/tmp/opencode/pb-mysql-runtime-qa}"
mysql_image="${MYSQL_QA_IMAGE:-mysql:8.4}"
mysql_password="${MYSQL_QA_PASSWORD:-pbpass}"
mysql_database="${MYSQL_QA_DATABASE:-pocketbase}"

mkdir -p "$tmp_dir"

cleanup() {
	if [ -f "$tmp_dir/pb.pid" ]; then
		kill "$(cat "$tmp_dir/pb.pid")" 2>/dev/null || true
	fi
	docker rm -f "$container_name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

cleanup

if ss -ltn "sport = :${http_addr##*:}" | grep -q LISTEN; then
	echo "HTTP address $http_addr is already in use." >&2
	exit 1
fi

docker run --rm -d \
	--name "$container_name" \
	-e MYSQL_ROOT_PASSWORD="$mysql_password" \
	-e MYSQL_DATABASE="$mysql_database" \
	-p "$mysql_port:3306" \
	"$mysql_image" >/dev/null

for _ in $(seq 1 90); do
	if docker exec "$container_name" mysql -h127.0.0.1 -uroot -p"$mysql_password" "$mysql_database" -e "SELECT 1" >/dev/null 2>&1; then
		break
	fi
	sleep 1
done
docker exec "$container_name" mysql -h127.0.0.1 -uroot -p"$mysql_password" "$mysql_database" -e "SELECT 1" >/dev/null

rm -rf "$tmp_dir/pb_data" "$tmp_dir/pb_migrations" "$tmp_dir/pb.log" "$tmp_dir/pocketbase-qa"
dsn="root:${mysql_password}@tcp(127.0.0.1:${mysql_port})/${mysql_database}?parseTime=true&multiStatements=true"

go build -o "$tmp_dir/pocketbase-qa" ./examples/base

PB_DATABASE_DRIVER=mysql PB_DATABASE_DSN="$dsn" \
	"$tmp_dir/pocketbase-qa" serve --dir "$tmp_dir/pb_data" --http "$http_addr" \
	> "$tmp_dir/pb.log" 2>&1 &
echo $! > "$tmp_dir/pb.pid"

for _ in $(seq 1 90); do
	if grep -q "Server started" "$tmp_dir/pb.log"; then
		break
	fi
	if ! kill -0 "$(cat "$tmp_dir/pb.pid")" 2>/dev/null; then
		cat "$tmp_dir/pb.log"
		exit 1
	fi
	sleep 1
done

PB_DATABASE_DRIVER=mysql PB_DATABASE_DSN="$dsn" \
	"$tmp_dir/pocketbase-qa" superuser upsert qa@example.com password123 --dir "$tmp_dir/pb_data" \
	> "$tmp_dir/superuser.log" 2>&1

base_url="http://${http_addr}"
token="$(curl -sS -X POST "$base_url/api/collections/_superusers/auth-with-password" \
	-H 'Content-Type: application/json' \
	--data '{"identity":"qa@example.com","password":"password123"}' | jq -r '.token')"

if [ -z "$token" ] || [ "$token" = "null" ]; then
	echo "Failed to authenticate QA superuser" >&2
	exit 1
fi

curl -sS -f -X POST "$base_url/api/collections" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data '{"name":"qa_runtime","type":"base","listRule":"","viewRule":"","createRule":"","updateRule":"","deleteRule":"","fields":[{"name":"title","type":"text","required":true,"max":255},{"name":"published","type":"bool"}],"indexes":["CREATE INDEX idx_qa_runtime_title ON qa_runtime (title)"]}' \
	> "$tmp_dir/create_collection.json"

curl -sS -f -X POST "$base_url/api/collections/qa_runtime/records" \
	-H 'Content-Type: application/json' \
	--data '{"title":"hello mysql runtime","published":true}' \
	> "$tmp_dir/create_record.json"

curl -sS -f "$base_url/api/collections/qa_runtime/records?sort=title" \
	> "$tmp_dir/list_records.json"

curl -sS -f "$base_url/api/collections/qa_runtime/records?filter=title~%22runtime%22" \
	> "$tmp_dir/filter_records.json"

if grep -q "ERROR" "$tmp_dir/pb.log"; then
	echo "Runtime QA completed but server log contains ERROR entries:" >&2
	grep "ERROR" "$tmp_dir/pb.log" >&2
	exit 1
fi

echo "MySQL runtime QA passed. Logs: $tmp_dir/pb.log"
