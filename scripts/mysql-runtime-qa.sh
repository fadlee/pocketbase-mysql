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

collection_id="$(jq -r '.id' "$tmp_dir/create_collection.json")"
jq '.fields += [{"name":"status","type":"select","required":false,"values":["draft","published"],"maxSelect":1}] | {fields:.fields}' \
	"$tmp_dir/create_collection.json" > "$tmp_dir/add_select_payload.json"

curl -sS -f -X PATCH "$base_url/api/collections/${collection_id}" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data @"$tmp_dir/add_select_payload.json" \
	> "$tmp_dir/update_collection_add_select.json"

curl -sS -f -X POST "$base_url/api/collections/qa_runtime/records" \
	-H 'Content-Type: application/json' \
	--data '{"title":"schema updated","published":false,"status":"draft"}' \
	> "$tmp_dir/create_record_after_schema_update.json"

curl -sS -f "$base_url/api/collections/qa_runtime/records?filter=status=%22draft%22" \
	> "$tmp_dir/filter_select_records.json"

curl -sS -f -X POST "$base_url/api/collections" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data '{"name":"qa_multi_select","type":"base","listRule":"","viewRule":"","createRule":"","updateRule":"","deleteRule":"","fields":[{"name":"title","type":"text","required":true,"max":255},{"name":"tags","type":"select","required":false,"values":["alpha","beta","gamma"],"maxSelect":3}]}' \
	> "$tmp_dir/create_multi_select_collection.json"

curl -sS -f -X POST "$base_url/api/collections/qa_multi_select/records" \
	-H 'Content-Type: application/json' \
	--data '{"title":"multi select","tags":["alpha","beta"]}' \
	> "$tmp_dir/create_multi_select_record.json"

curl -sS -f "$base_url/api/collections/qa_multi_select/records?filter=tags~%22alpha%22" \
	> "$tmp_dir/filter_multi_select_records.json"

matrix_id="$(jq -r '.id' "$tmp_dir/create_multi_select_collection.json")"
curl -sS -f -X POST "$base_url/api/collections" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data '{"name":"qa_matrix","type":"base","listRule":"","viewRule":"","createRule":"","updateRule":"","deleteRule":"","fields":[{"name":"title","type":"text","required":true,"max":255},{"name":"status","type":"select","required":false,"values":["draft","published"],"maxSelect":1}]}' \
	> "$tmp_dir/create_matrix_collection.json"

matrix_id="$(jq -r '.id' "$tmp_dir/create_matrix_collection.json")"

curl -sS -f -X POST "$base_url/api/collections/qa_matrix/records" \
	-H 'Content-Type: application/json' \
	--data '{"title":"before matrix","status":"draft"}' \
	> "$tmp_dir/create_matrix_record.json"

jq '(.fields[] | select(.name == "status") | .name) = "state" | {fields:.fields}' \
	"$tmp_dir/create_matrix_collection.json" > "$tmp_dir/matrix_rename_payload.json"

curl -sS -f -X PATCH "$base_url/api/collections/${matrix_id}" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data @"$tmp_dir/matrix_rename_payload.json" \
	> "$tmp_dir/matrix_rename_result.json"
sleep 1

jq '.fields |= map(select(.name != "title")) | {fields:.fields}' \
	"$tmp_dir/matrix_rename_result.json" > "$tmp_dir/matrix_delete_payload.json"

curl -sS -f -X PATCH "$base_url/api/collections/${matrix_id}" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data @"$tmp_dir/matrix_delete_payload.json" \
	> "$tmp_dir/matrix_delete_result.json"
sleep 1

jq '(.fields[] | select(.name == "state") | .maxSelect) = 3 | (.fields[] | select(.name == "state") | .values) = ["draft","published","archived"] | {fields:.fields}' \
	"$tmp_dir/matrix_delete_result.json" > "$tmp_dir/matrix_single_to_multi_payload.json"

curl -sS -f -X PATCH "$base_url/api/collections/${matrix_id}" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data @"$tmp_dir/matrix_single_to_multi_payload.json" \
	> "$tmp_dir/matrix_single_to_multi_result.json"
sleep 1

curl -sS -f -X POST "$base_url/api/collections/qa_matrix/records" \
	-H 'Content-Type: application/json' \
	--data '{"state":["draft","published"]}' \
	> "$tmp_dir/create_matrix_multi_record.json"

curl -sS -f "$base_url/api/collections/qa_matrix/records?filter=state~%22draft%22" \
	> "$tmp_dir/filter_matrix_multi_records.json"

jq '(.fields[] | select(.name == "state") | .maxSelect) = 1 | {fields:.fields}' \
	"$tmp_dir/matrix_single_to_multi_result.json" > "$tmp_dir/matrix_multi_to_single_payload.json"

curl -sS -f -X PATCH "$base_url/api/collections/${matrix_id}" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data @"$tmp_dir/matrix_multi_to_single_payload.json" \
	> "$tmp_dir/matrix_multi_to_single_result.json"

curl -sS -f "$base_url/api/collections/qa_matrix/records?filter=state=%22published%22" \
	> "$tmp_dir/filter_matrix_single_records.json"

curl -sS -f -X POST "$base_url/api/collections" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data '{"name":"qa_authors","type":"base","listRule":"","viewRule":"","createRule":"","updateRule":"","deleteRule":"","fields":[{"name":"name","type":"text","required":true,"max":255}]}' \
	> "$tmp_dir/create_authors_collection.json"

authors_collection_id="$(jq -r '.id' "$tmp_dir/create_authors_collection.json")"

curl -sS -f -X POST "$base_url/api/collections" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data '{"name":"qa_books","type":"base","listRule":"","viewRule":"","createRule":"","updateRule":"","deleteRule":"","fields":[{"name":"title","type":"text","required":true,"max":255},{"name":"authors","type":"relation","required":false,"collectionId":"'"${authors_collection_id}"'","maxSelect":3}]}' \
	> "$tmp_dir/create_books_collection.json"

curl -sS -f -X POST "$base_url/api/collections/qa_authors/records" \
	-H 'Content-Type: application/json' \
	--data '{"name":"Author One"}' \
	> "$tmp_dir/create_author_one.json"

curl -sS -f -X POST "$base_url/api/collections/qa_authors/records" \
	-H 'Content-Type: application/json' \
	--data '{"name":"Author Two"}' \
	> "$tmp_dir/create_author_two.json"

author_one_id="$(jq -r '.id' "$tmp_dir/create_author_one.json")"
author_two_id="$(jq -r '.id' "$tmp_dir/create_author_two.json")"

curl -sS -f -X POST "$base_url/api/collections/qa_books/records" \
	-H 'Content-Type: application/json' \
	--data '{"title":"Book One","authors":["'"${author_one_id}"'","'"${author_two_id}"'"]}' \
	> "$tmp_dir/create_book_one.json"

curl -sS -f "$base_url/api/collections/qa_authors/records?filter=qa_books_via_authors.title~%22Book%22" \
	> "$tmp_dir/filter_back_relation_records.json"

curl -sS -f "$base_url/api/collections/qa_books/records?filter=authors.name~%22Author%22" \
	> "$tmp_dir/filter_forward_relation_records.json"

curl -sS -f "$base_url/api/collections/qa_books/records?expand=authors" \
	> "$tmp_dir/expand_relation_records.json"

curl -sS -f -X POST "$base_url/api/collections" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data '{"name":"qa_books_view","type":"view","viewQuery":"SELECT id, title FROM qa_books"}' \
	> "$tmp_dir/create_books_view_collection.json"

curl -sS -f "$base_url/api/collections/qa_books_view/records" \
	-H "Authorization: Bearer ${token}" \
	> "$tmp_dir/list_books_view_records.json"

curl -sS -f "$base_url/api/collections/qa_books_view/records?filter=title~%22Book%22" \
	-H "Authorization: Bearer ${token}" \
	> "$tmp_dir/filter_books_view_records.json"

curl -sS -f -X POST "$base_url/api/collections/qa_books/records" \
	-H 'Content-Type: application/json' \
	--data '{"title":"Book Two"}' \
	> "$tmp_dir/create_book_two.json"

curl -sS -f "$base_url/api/collections/qa_books_view/records?sort=title" \
	-H "Authorization: Bearer ${token}" \
	> "$tmp_dir/list_books_view_after_update.json"

if grep -q "ERROR" "$tmp_dir/pb.log"; then
	echo "Runtime QA completed but server log contains ERROR entries:" >&2
	grep "ERROR" "$tmp_dir/pb.log" >&2
	exit 1
fi

echo "MySQL runtime QA passed. Logs: $tmp_dir/pb.log"
