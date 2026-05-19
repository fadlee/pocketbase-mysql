#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

container_name="${MYSQL_QA_CONTAINER:-pb-mysql-runtime-qa}"
mysql_host="${MYSQL_QA_HOST:-127.0.0.1}"
mysql_port="${MYSQL_QA_MYSQL_PORT:-3307}"
mysql_user="${MYSQL_QA_USER:-root}"
http_addr="${MYSQL_QA_HTTP_ADDR:-127.0.0.1:18090}"
tmp_dir="${MYSQL_QA_TMP_DIR:-/tmp/opencode/pb-mysql-runtime-qa}"
mysql_image="${MYSQL_QA_IMAGE:-mysql:8.4}"
mysql_password="${MYSQL_QA_PASSWORD-pbpass}"
mysql_database="${MYSQL_QA_DATABASE:-pocketbase}"
skip_docker="${MYSQL_QA_SKIP_DOCKER:-0}"

mkdir -p "$tmp_dir"

cleanup() {
	if [ -f "$tmp_dir/pb.pid" ]; then
		kill "$(cat "$tmp_dir/pb.pid")" 2>/dev/null || true
	fi
	if [ "$skip_docker" != "1" ]; then
		docker rm -f "$container_name" >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

cleanup

if { command -v ss >/dev/null 2>&1 && ss -ltn "sport = :${http_addr##*:}" | grep -q LISTEN; } || \
   netstat -an 2>/dev/null | grep -q ":${http_addr##*:}.*LISTEN"; then
	echo "HTTP address $http_addr is already in use." >&2
	exit 1
fi

if [ "$skip_docker" = "1" ]; then
	echo "Skipping Docker - using existing MySQL at ${mysql_host}:${mysql_port}"
	# Verify connection is reachable
	if command -v mysql >/dev/null 2>&1; then
		mysql -h"$mysql_host" -P"$mysql_port" -u"$mysql_user" \
			${mysql_password:+-p"$mysql_password"} \
			"$mysql_database" -e "SELECT 1" >/dev/null 2>&1 \
			|| { echo "Cannot connect to MySQL at ${mysql_host}:${mysql_port}" >&2; exit 1; }
	fi
else
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
fi

rm -rf "$tmp_dir/pb_data" "$tmp_dir/pb_migrations" "$tmp_dir/pb.log" "$tmp_dir/pocketbase-qa"
if [ -z "$mysql_password" ]; then
	dsn="${mysql_user}@tcp(${mysql_host}:${mysql_port})/${mysql_database}?parseTime=true&multiStatements=true"
else
	dsn="${mysql_user}:${mysql_password}@tcp(${mysql_host}:${mysql_port})/${mysql_database}?parseTime=true&multiStatements=true"
fi

go build -o "$tmp_dir/pocketbase-qa" ./examples/base

PB_DATABASE_DRIVER=mysql PB_DATABASE_DSN="$dsn" \
	"$tmp_dir/pocketbase-qa" serve --dir "$tmp_dir/pb_data" --migrationsDir "$tmp_dir/pb_migrations" --http "$http_addr" \
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
	"$tmp_dir/pocketbase-qa" superuser upsert qa@example.com password123 --dir "$tmp_dir/pb_data" --migrationsDir "$tmp_dir/pb_migrations" \
	> "$tmp_dir/superuser.log" 2>&1

base_url="http://${http_addr}"
token="$(curl -sS -X POST "$base_url/api/collections/_superusers/auth-with-password" \
	-H 'Content-Type: application/json' \
	--data '{"identity":"qa@example.com","password":"password123"}' | jq -r '.token')"

if [ -z "$token" ] || [ "$token" = "null" ]; then
	echo "Failed to authenticate QA superuser" >&2
	exit 1
fi

# Allow rerunning the QA against the same MySQL database by removing previous QA collections.
for cleanup_pass in $(seq 1 10); do
	curl -sS -f "$base_url/api/collections?page=1&perPage=500" \
		-H "Authorization: Bearer ${token}" \
		> "$tmp_dir/existing_collections.json"

	mapfile -t qa_collection_ids < <(jq -r '.items | sort_by(.created) | reverse | .[] | select(.name | startswith("qa_")) | .id' "$tmp_dir/existing_collections.json" | tr -d '\r')
	[ "${#qa_collection_ids[@]}" -eq 0 ] && break

	deleted_any=0
	for collection_id_to_delete in "${qa_collection_ids[@]}"; do
		[ -z "$collection_id_to_delete" ] && continue
		if curl -sS -f -X DELETE "$base_url/api/collections/${collection_id_to_delete}" \
			-H "Authorization: Bearer ${token}" \
			> /dev/null 2>&1; then
			deleted_any=1
		fi
	done

	if [ "$deleted_any" = "0" ]; then
		echo "Failed to cleanup previous qa_* collections" >&2
		exit 1
	fi
done

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

# ============================================================
# All field types test
# ============================================================

echo "Testing all field types..."

# Create a file collection for relation reference
curl -sS -f -X POST "$base_url/api/collections" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data '{"name":"qa_ref","type":"base","listRule":"","viewRule":"","createRule":"","updateRule":"","deleteRule":"","fields":[{"name":"label","type":"text","required":true}]}' \
	> "$tmp_dir/create_qa_ref.json"

qa_ref_id="$(jq -r '.id' "$tmp_dir/create_qa_ref.json")"

curl -sS -f -X POST "$base_url/api/collections/qa_ref/records" \
	-H 'Content-Type: application/json' \
	--data '{"label":"ref-one"}' \
	> "$tmp_dir/create_qa_ref_record.json"

qa_ref_record_id="$(jq -r '.id' "$tmp_dir/create_qa_ref_record.json")"

# Create collection with all supported field types
curl -sS -f -X POST "$base_url/api/collections" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data '{
		"name":"qa_all_fields",
		"type":"base",
		"listRule":"",
		"viewRule":"",
		"createRule":"",
		"updateRule":"",
		"deleteRule":"",
		"fields":[
			{"name":"f_text","type":"text","required":false,"max":500},
			{"name":"f_number","type":"number","required":false},
			{"name":"f_bool","type":"bool","required":false},
			{"name":"f_email","type":"email","required":false},
			{"name":"f_url","type":"url","required":false},
			{"name":"f_date","type":"date","required":false},
			{"name":"f_select_single","type":"select","required":false,"values":["a","b","c"],"maxSelect":1},
			{"name":"f_select_multi","type":"select","required":false,"values":["x","y","z"],"maxSelect":3},
			{"name":"f_json","type":"json","required":false},
			{"name":"f_editor","type":"editor","required":false},
			{"name":"f_relation","type":"relation","required":false,"collectionId":"'"${qa_ref_id}"'","maxSelect":1},
			{"name":"f_relation_multi","type":"relation","required":false,"collectionId":"'"${qa_ref_id}"'","maxSelect":5}
		]
	}' \
	> "$tmp_dir/create_qa_all_fields.json"

qa_all_fields_id="$(jq -r '.id' "$tmp_dir/create_qa_all_fields.json")"

# Create record with all fields populated
curl -sS -f -X POST "$base_url/api/collections/qa_all_fields/records" \
	-H 'Content-Type: application/json' \
	--data '{
		"f_text":"hello world",
		"f_number":43,
		"f_bool":true,
		"f_email":"test@example.com",
		"f_url":"https://example.com",
		"f_date":"2026-01-15 10:00:00.000Z",
		"f_select_single":"a",
		"f_select_multi":["x","y"],
		"f_json":{"key":"value","num":123},
		"f_editor":"<p>rich text</p>",
		"f_relation":"'"${qa_ref_record_id}"'",
		"f_relation_multi":["'"${qa_ref_record_id}"'"]
	}' \
	> "$tmp_dir/create_qa_all_fields_record.json"

qa_all_fields_record_id="$(jq -r '.id' "$tmp_dir/create_qa_all_fields_record.json")"

# Read back the record and verify fields
curl -sS -f "$base_url/api/collections/qa_all_fields/records/${qa_all_fields_record_id}" \
	> "$tmp_dir/get_qa_all_fields_record.json"

# Verify each field value
jq -e '.f_text == "hello world"' "$tmp_dir/get_qa_all_fields_record.json" > /dev/null || { echo "FAIL: f_text mismatch" >&2; exit 1; }
jq -e '.f_number == 43' "$tmp_dir/get_qa_all_fields_record.json" > /dev/null || { echo "FAIL: f_number mismatch" >&2; exit 1; }
jq -e '.f_bool == true' "$tmp_dir/get_qa_all_fields_record.json" > /dev/null || { echo "FAIL: f_bool mismatch" >&2; exit 1; }
jq -e '.f_email == "test@example.com"' "$tmp_dir/get_qa_all_fields_record.json" > /dev/null || { echo "FAIL: f_email mismatch" >&2; exit 1; }
jq -e '.f_url == "https://example.com"' "$tmp_dir/get_qa_all_fields_record.json" > /dev/null || { echo "FAIL: f_url mismatch" >&2; exit 1; }
jq -e '.f_select_single == "a"' "$tmp_dir/get_qa_all_fields_record.json" > /dev/null || { echo "FAIL: f_select_single mismatch" >&2; exit 1; }
jq -e '.f_select_multi | length == 2' "$tmp_dir/get_qa_all_fields_record.json" > /dev/null || { echo "FAIL: f_select_multi mismatch" >&2; exit 1; }
jq -e '.f_editor == "<p>rich text</p>"' "$tmp_dir/get_qa_all_fields_record.json" > /dev/null || { echo "FAIL: f_editor mismatch" >&2; exit 1; }
jq -e '.f_relation == "'"${qa_ref_record_id}"'"' "$tmp_dir/get_qa_all_fields_record.json" > /dev/null || { echo "FAIL: f_relation mismatch" >&2; exit 1; }
jq -e '.f_relation_multi | length == 1' "$tmp_dir/get_qa_all_fields_record.json" > /dev/null || { echo "FAIL: f_relation_multi mismatch" >&2; exit 1; }

echo "All field type create/read: OK"

# Update record - change several fields
curl -sS -f -X PATCH "$base_url/api/collections/qa_all_fields/records/${qa_all_fields_record_id}" \
	-H 'Content-Type: application/json' \
	--data '{
		"f_text":"updated text",
		"f_number":99,
		"f_bool":false,
		"f_select_single":"b",
		"f_select_multi":["z"]
	}' \
	> "$tmp_dir/update_qa_all_fields_record.json"

jq -e '.f_text == "updated text"' "$tmp_dir/update_qa_all_fields_record.json" > /dev/null || { echo "FAIL: f_text update mismatch" >&2; exit 1; }
jq -e '.f_number == 99' "$tmp_dir/update_qa_all_fields_record.json" > /dev/null || { echo "FAIL: f_number update mismatch" >&2; exit 1; }
jq -e '.f_bool == false' "$tmp_dir/update_qa_all_fields_record.json" > /dev/null || { echo "FAIL: f_bool update mismatch" >&2; exit 1; }
jq -e '.f_select_single == "b"' "$tmp_dir/update_qa_all_fields_record.json" > /dev/null || { echo "FAIL: f_select_single update mismatch" >&2; exit 1; }
jq -e '.f_select_multi == ["z"]' "$tmp_dir/update_qa_all_fields_record.json" > /dev/null || { echo "FAIL: f_select_multi update mismatch" >&2; exit 1; }

echo "All field type update: OK"

# Filter by each field type
curl -sS -f "$base_url/api/collections/qa_all_fields/records?filter=f_text~%22updated%22" \
	> "$tmp_dir/filter_by_text.json"
jq -e '.items | length >= 1' "$tmp_dir/filter_by_text.json" > /dev/null || { echo "FAIL: filter by f_text" >&2; exit 1; }

curl -sS -f "$base_url/api/collections/qa_all_fields/records?filter=f_number%3E50" \
	> "$tmp_dir/filter_by_number.json"
jq -e '.items | length >= 1' "$tmp_dir/filter_by_number.json" > /dev/null || { echo "FAIL: filter by f_number" >&2; exit 1; }

curl -sS -f "$base_url/api/collections/qa_all_fields/records?filter=f_bool%3Dfalse" \
	> "$tmp_dir/filter_by_bool.json"
jq -e '.items | length >= 1' "$tmp_dir/filter_by_bool.json" > /dev/null || { echo "FAIL: filter by f_bool" >&2; exit 1; }

curl -sS -f "$base_url/api/collections/qa_all_fields/records?filter=f_email~%22example%22" \
	> "$tmp_dir/filter_by_email.json"
jq -e '.items | length >= 1' "$tmp_dir/filter_by_email.json" > /dev/null || { echo "FAIL: filter by f_email" >&2; exit 1; }

curl -sS -f "$base_url/api/collections/qa_all_fields/records?filter=f_select_single%3D%22b%22" \
	> "$tmp_dir/filter_by_select.json"
jq -e '.items | length >= 1' "$tmp_dir/filter_by_select.json" > /dev/null || { echo "FAIL: filter by f_select_single" >&2; exit 1; }

curl -sS -f "$base_url/api/collections/qa_all_fields/records?filter=f_relation%3D%22${qa_ref_record_id}%22" \
	> "$tmp_dir/filter_by_relation.json"
jq -e '.items | length >= 1' "$tmp_dir/filter_by_relation.json" > /dev/null || { echo "FAIL: filter by f_relation" >&2; exit 1; }

echo "All field type filters: OK"

# Schema update: add a new field to qa_all_fields
curl -sS -f -X PATCH "$base_url/api/collections/${qa_all_fields_id}" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data '{"fields":[
		{"name":"f_text","type":"text","required":false,"max":500},
		{"name":"f_number","type":"number","required":false},
		{"name":"f_bool","type":"bool","required":false},
		{"name":"f_email","type":"email","required":false},
		{"name":"f_url","type":"url","required":false},
		{"name":"f_date","type":"date","required":false},
		{"name":"f_select_single","type":"select","required":false,"values":["a","b","c"],"maxSelect":1},
		{"name":"f_select_multi","type":"select","required":false,"values":["x","y","z"],"maxSelect":3},
		{"name":"f_json","type":"json","required":false},
		{"name":"f_editor","type":"editor","required":false},
		{"name":"f_relation","type":"relation","required":false,"collectionId":"'"${qa_ref_id}"'","maxSelect":1},
		{"name":"f_relation_multi","type":"relation","required":false,"collectionId":"'"${qa_ref_id}"'","maxSelect":5},
		{"name":"f_new_text","type":"text","required":false,"max":100}
	]}' \
	> "$tmp_dir/schema_add_field.json"

# Create record using new field
curl -sS -f -X POST "$base_url/api/collections/qa_all_fields/records" \
	-H 'Content-Type: application/json' \
	--data '{"f_text":"after schema add","f_new_text":"new field value"}' \
	> "$tmp_dir/create_after_schema_add.json"

jq -e '.f_new_text == "new field value"' "$tmp_dir/create_after_schema_add.json" > /dev/null || { echo "FAIL: f_new_text after schema add" >&2; exit 1; }

echo "Schema add field: OK"
sleep 1

# Schema update: rename f_new_text -> f_renamed_text
curl -sS -f -X PATCH "$base_url/api/collections/${qa_all_fields_id}" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data '{"fields":[
		{"name":"f_text","type":"text","required":false,"max":500},
		{"name":"f_number","type":"number","required":false},
		{"name":"f_bool","type":"bool","required":false},
		{"name":"f_email","type":"email","required":false},
		{"name":"f_url","type":"url","required":false},
		{"name":"f_date","type":"date","required":false},
		{"name":"f_select_single","type":"select","required":false,"values":["a","b","c"],"maxSelect":1},
		{"name":"f_select_multi","type":"select","required":false,"values":["x","y","z"],"maxSelect":3},
		{"name":"f_json","type":"json","required":false},
		{"name":"f_editor","type":"editor","required":false},
		{"name":"f_relation","type":"relation","required":false,"collectionId":"'"${qa_ref_id}"'","maxSelect":1},
		{"name":"f_relation_multi","type":"relation","required":false,"collectionId":"'"${qa_ref_id}"'","maxSelect":5},
		{"name":"f_renamed_text","type":"text","required":false,"max":100}
	]}' \
	> "$tmp_dir/schema_rename_field.json"

sleep 1

# Verify old records still readable after rename
curl -sS -f "$base_url/api/collections/qa_all_fields/records/${qa_all_fields_record_id}" \
	> "$tmp_dir/get_after_rename.json"
jq -e '.f_text == "updated text"' "$tmp_dir/get_after_rename.json" > /dev/null || { echo "FAIL: existing record unreadable after rename" >&2; exit 1; }

echo "Schema rename field: OK"

# Schema update: delete f_renamed_text
curl -sS -f -X PATCH "$base_url/api/collections/${qa_all_fields_id}" \
	-H "Authorization: Bearer ${token}" \
	-H 'Content-Type: application/json' \
	--data '{"fields":[
		{"name":"f_text","type":"text","required":false,"max":500},
		{"name":"f_number","type":"number","required":false},
		{"name":"f_bool","type":"bool","required":false},
		{"name":"f_email","type":"email","required":false},
		{"name":"f_url","type":"url","required":false},
		{"name":"f_date","type":"date","required":false},
		{"name":"f_select_single","type":"select","required":false,"values":["a","b","c"],"maxSelect":1},
		{"name":"f_select_multi","type":"select","required":false,"values":["x","y","z"],"maxSelect":3},
		{"name":"f_json","type":"json","required":false},
		{"name":"f_editor","type":"editor","required":false},
		{"name":"f_relation","type":"relation","required":false,"collectionId":"'"${qa_ref_id}"'","maxSelect":1},
		{"name":"f_relation_multi","type":"relation","required":false,"collectionId":"'"${qa_ref_id}"'","maxSelect":5}
	]}' \
	> "$tmp_dir/schema_delete_field.json"

sleep 1

# Verify records still readable after delete
curl -sS -f "$base_url/api/collections/qa_all_fields/records/${qa_all_fields_record_id}" \
	> "$tmp_dir/get_after_delete_field.json"
jq -e '.f_text == "updated text"' "$tmp_dir/get_after_delete_field.json" > /dev/null || { echo "FAIL: existing record unreadable after field delete" >&2; exit 1; }
jq -e 'has("f_renamed_text") | not' "$tmp_dir/get_after_delete_field.json" > /dev/null || { echo "FAIL: deleted field still present" >&2; exit 1; }

echo "Schema delete field: OK"
sleep 1

# Sort by each field type
curl -sS -f "$base_url/api/collections/qa_all_fields/records?sort=f_text" > /dev/null || { echo "FAIL: sort by f_text" >&2; exit 1; }
curl -sS -f "$base_url/api/collections/qa_all_fields/records?sort=-f_number" > /dev/null || { echo "FAIL: sort by f_number" >&2; exit 1; }
curl -sS -f "$base_url/api/collections/qa_all_fields/records?sort=-f_date" > /dev/null || { echo "FAIL: sort by f_date" >&2; exit 1; }
curl -sS -f "$base_url/api/collections/qa_all_fields/records?sort=-id" > /dev/null || { echo "FAIL: sort by -id" >&2; exit 1; }

echo "All field type sorts: OK"

# Expand relation
curl -sS -f "$base_url/api/collections/qa_all_fields/records?expand=f_relation" \
	> "$tmp_dir/expand_all_fields_relation.json"
jq -e '.items[] | select(.expand.f_relation.label == "ref-one")' "$tmp_dir/expand_all_fields_relation.json" > /dev/null || { echo "FAIL: expand f_relation" >&2; exit 1; }

echo "Relation expand: OK"

echo "All field types QA: PASSED"

# ============================================================

if grep -q "ERROR" "$tmp_dir/pb.log"; then
	echo "Runtime QA completed but server log contains ERROR entries:" >&2
	grep "ERROR" "$tmp_dir/pb.log" >&2
	exit 1
fi

echo "MySQL runtime QA passed. Logs: $tmp_dir/pb.log"
