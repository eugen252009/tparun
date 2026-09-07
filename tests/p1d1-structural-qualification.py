#!/usr/bin/env python3
"""Local-only Q-A qualification; builds the exact v2 release in a temporary tree.
No deploy script is invoked. Immutable fixtures are constructed by INSERT, never
by disabling triggers or CHECKs. All comparisons fail closed via assertions.
"""
import hashlib
import os
from pathlib import Path
import re
import shutil
import sqlite3
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
V2 = 'c73ecd8533da5a8efd2eeda019755a950489c6fe'
DOMAINS = {
    ('accounts', 'status'): ['active', 'suspended'],
    ('repositories', 'visibility'): ['public', 'private'],
    ('billing_purchases', 'status'): ['pending', 'superseded', 'paid', 'failed', 'expired', 'reconciliation_required'],
    ('billing_provider_events', 'status'): ['processing', 'processed', 'reconciliation_required'],
    ('product_grants', 'period_cadence'): ['annual', 'monthly', 'continuous'],
    ('purchase_grant_snapshots', 'period_cadence'): ['annual', 'monthly', 'continuous'],
    ('purchase_grant_instances', 'period_cadence'): ['annual', 'monthly', 'continuous'],
    ('publish_jobs', 'state'): ['queued', 'running', 'succeeded', 'failed'],
    ('publish_jobs', 'cleanup_state'): ['complete', 'pending'],
    ('egress_accounting_uncertain', 'state'): ['streaming', 'uncertain'],
}
NEGATIVE = ('egress_accounting_uncertain', 'reserved_bytes')
INVALID = {k: ('weekly' if k[1] == 'period_cadence' else 'internal' if k[1] == 'visibility' else 'paused' if k == ('publish_jobs', 'state') else 'unknown') for k in DOMAINS}
INVALID[NEGATIVE] = -1
NAMES = ['accounts-status', 'repositories-visibility', 'billing-purchases-status', 'billing-provider-events-status', 'product-grants-cadence', 'purchase-grant-snapshots-cadence', 'purchase-grant-instances-cadence', 'publish-job-state', 'publish-job-cleanup-state', 'egress-uncertain-state']


def run(*args, **kw):
    return subprocess.run(args, check=True, text=True, stdout=subprocess.PIPE, **kw).stdout.strip()


def connect(path):
    c = sqlite3.connect(path)
    c.execute('PRAGMA foreign_keys=ON')
    return c


def healthy(c):
    assert c.execute('PRAGMA integrity_check').fetchall() == [('ok',)]
    assert c.execute('PRAGMA foreign_key_check').fetchall() == []


def tables(c):
    return [r[0] for r in c.execute("SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")]


def facts(c):
    return {t: sorted(c.execute(f'SELECT * FROM "{t}"').fetchall(), key=repr) for t in tables(c)}


def normalize(sql):
    # SQLite quotes identifiers after ALTER TABLE. Only remove identifier quotes;
    # string literals and their whitespace remain byte-exact.
    parts = re.split("('(?:''|[^'])*')", sql or '')
    return ''.join(p if i % 2 else re.sub(r'\s+', '', p.replace('"', '')).lower() for i, p in enumerate(parts))


def metadata(c):
    result = {}
    for t in tables(c):
        indexes = {}
        for _, name, unique, origin, partial in c.execute(f'PRAGMA index_list("{t}")'):
            indexes[name] = (unique, origin, partial, c.execute(f'PRAGMA index_xinfo("{name}")').fetchall(), normalize(c.execute('SELECT sql FROM sqlite_schema WHERE name=?', (name,)).fetchone()[0]))
        result[t] = dict(columns=c.execute(f'PRAGMA table_xinfo("{t}")').fetchall(),
                         fks=sorted(c.execute(f'PRAGMA foreign_key_list("{t}")').fetchall()),
                         indexes=indexes,
                         triggers=c.execute("SELECT name,tbl_name,sql FROM sqlite_schema WHERE type='trigger' AND tbl_name=? ORDER BY name", (t,)).fetchall(),
                         sql=normalize(c.execute('SELECT sql FROM sqlite_schema WHERE name=?', (t,)).fetchone()[0]))
        result[t]['triggers'] = [(n, owner, normalize(s)) for n, owner, s in result[t]['triggers']]
    return result


def seed(path, override=None):
    c = connect(path)
    now, until = '2026-01-01T00:00:00Z', '2027-01-01T00:00:00Z'
    def insert(t, **row):
        if override and override[0][0] == t:
            row[override[0][1]] = override[1]
        c.execute(f'INSERT INTO {t} ({",".join(row)}) VALUES ({",".join("?" for _ in row)})', tuple(row.values()))
    insert('accounts', id='a', email='a@example.invalid', password_hash='x', status='active', created_at=now)
    insert('account_metadata', account_id='a', account_class='unclassified')
    insert('repositories', id='r', account_id='a', name='repo', visibility='public', created_at=now)
    # Pending while constructing immutable children; requested purchase status is
    # applied last, without changing provider_state or purchase terms.
    purchase_override = override if override and override[0][0] == 'billing_purchases' else None
    if purchase_override:
        override = None
    insert('billing_purchases', id='p', account_id='a', product_id='future-product', plan_code='future-offer', rate_card_id='future-rate', rate_card_mode='test', currency='EUR', amount_minor=1, billing_environment='future-environment', provider='future-provider', status='pending', provider_state='open', entitlement_period_start=now, entitlement_period_end=until, created_at=now, updated_at=now)
    cadence = 'monthly'
    if override and override[0] == ('purchase_grant_snapshots', 'period_cadence') and override[1] in DOMAINS[override[0]]:
        cadence = override[1]
    insert('purchase_grant_snapshot_sets', purchase_id='p', expected_count=1, capability_digest=hashlib.sha256(f'future-capability|{cadence}|0|0||0'.encode()).hexdigest())
    insert('purchase_grant_snapshots', purchase_id='p', capability_id='future-capability', period_cadence='monthly')
    insert('purchase_grant_instances', customer_id='a', product_id='future-product', capability_id='future-capability', purchase_id='p', offer_id='future-offer', enabled=1, valid_from=now, valid_until=until, period_cadence='monthly', created_at=now, updated_at=now)
    insert('product_grants', customer_id='a', product_id='future-product', capability_id='future-capability', offer_id='future-offer', enabled=1, valid_from=now, valid_until=until, period_cadence='monthly', source='future-source', created_at=now, updated_at=now)
    insert('billing_provider_events', provider='future-provider', environment='future-environment', provider_event_id='event', event_type='future-event', received_at=now, status='processing')
    insert('publish_jobs', id='j', repository_id='r', state='queued', cleanup_state='complete', incoming_path='local', created_at=now)
    insert('egress_accounting_uncertain', id='e', repository_id='r', state='streaming', reserved_bytes=0, created_at=now)
    insert('operator_identity', id=1, user_handle=b'qa-operator')
    insert('operator_credentials', credential_id=b'qa-credential', credential_data=b'qa-data', created_at=now)
    if purchase_override:
        c.execute(f'UPDATE billing_purchases SET {purchase_override[0][1]}=?', (purchase_override[1],))
    c.commit()
    healthy(c)
    c.close()


def start(binary, directory, version):
    directory.mkdir(exist_ok=True)
    env = dict(os.environ, TPAHUB_DATA_DIR=str(directory), TPAHUB_LISTEN='127.0.0.1:0')
    p = subprocess.Popen([str(binary), 'serve'], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    try:
        for _ in range(200):
            if p.poll() is not None:
                raise AssertionError(p.stderr.read().decode())
            try:
                if not (directory / 'tpahub.db').exists():
                    time.sleep(.05)
                    continue
                with connect(directory / 'tpahub.db') as c:
                    if c.execute('SELECT version FROM tpahub_schema').fetchall() == [(version,)]:
                        time.sleep(.2)
                        assert p.poll() is None
                        return
            except sqlite3.OperationalError:
                pass
            time.sleep(.05)
        raise AssertionError('startup timeout')
    finally:
        p.terminate()
        p.communicate(timeout=10)


def main(work):
    src = work / 'v2-src'
    src.mkdir()
    archive = subprocess.Popen(['git', 'archive', V2], cwd=ROOT, stdout=subprocess.PIPE)
    subprocess.run(['tar', '-x', '-C', str(src)], stdin=archive.stdout, check=True)
    archive.stdout.close()
    assert archive.wait() == 0
    old, candidate = work / 'v2', work / 'candidate'
    run('go', 'build', '-o', str(old), './cmd/tpahub', cwd=src)
    run('go', 'build', '-o', str(candidate), './cmd/tpahub', cwd=ROOT)
    start(old, work / 'empty-v2', 2)
    start(candidate, work / 'empty-v3', 3)
    def lineage(name, base, migrate_binary=None, set_version=None):
        directory = work / name
        directory.mkdir()
        db = directory / 'tpahub.db'
        shutil.copyfile(work / base / 'tpahub.db', db)
        if set_version is not None:
            with connect(db) as c:
                c.execute('UPDATE tpahub_schema SET version=?', (set_version,))
                c.commit()
        if migrate_binary:
            run(str(migrate_binary), 'migrate', env=dict(os.environ, TPAHUB_DATA_DIR=str(directory)))
        with connect(db) as c:
            present = c.execute("SELECT count(*) FROM sqlite_schema WHERE type='index' AND name='billing_purchases_pending'").fetchone()[0] == 1
            count = c.execute("SELECT count(*) FROM sqlite_schema WHERE type='index'").fetchone()[0]
            return db, present, count, metadata(c)
    l1 = lineage('lineage-fresh-v2', 'empty-v2')
    l2 = lineage('lineage-migrated-v2', 'empty-v2', old, 1)
    l3 = lineage('lineage-fresh-v3', 'empty-v3')
    l4a = lineage('lineage-fresh-v2-v3', 'empty-v2', candidate, 2)
    # L4-B starts from the actual historical migrated-v2 file.
    l4b = lineage('lineage-migrated-v2-v3', 'lineage-migrated-v2', candidate, 2)
    for label, item in [('Fresh c73ecd', l1), ('c73ecd v1→v2', l2), ('Fresh current', l3), ('Fresh-v2→v3', l4a), ('Migrated-v2→v3', l4b)]:
        print(f'LINEAGE {label} schema={sqlite3.connect(item[0]).execute("SELECT version FROM tpahub_schema").fetchone()[0]} billing_purchases_pending={"YES" if item[1] else "NO"} indexes={item[2]}')
    assert not l1[1] and l1[2] == 37 and l2[1] and l2[2] == 38
    assert l3[1] and l4a[1] and l4b[1]
    assert l3[2] == l4a[2] == l4b[2]
    v2_drift = [(t, field) for t in l1[3] for field in l1[3][t] if l1[3][t][field] != l2[3][t][field]]
    print('FRESH_V2_MIGRATED_V2_STRUCTURAL_DIFFERENCES ' + repr(v2_drift))
    assert v2_drift == [('billing_purchases', 'indexes')]
    assert l3[3] == l4a[3] == l4b[3]
    print('PASS fresh-migrated-current-schema-structural-parity')
    print('PASS current-schema-index-authority-parity')
    def fixture(name, version, override=None):
        directory = work / name
        directory.mkdir()
        db = directory / 'tpahub.db'
        if version == '2m':
            shutil.copyfile(work / 'empty-v2' / 'tpahub.db', db)
            with connect(db) as c:
                c.execute('UPDATE tpahub_schema SET version=1')
                c.commit()
            run(str(old), 'migrate', env=dict(os.environ, TPAHUB_DATA_DIR=str(directory)))
        else:
            shutil.copyfile(work / f'empty-v{version}' / 'tpahub.db', db)
        if override and override[0] == (('billing_purchases', 'provider_state')):
            seed(db)
            with connect(db) as c:
                c.execute('UPDATE billing_purchases SET provider_state=?', (override[1],))
                c.commit()
        else:
            seed(db, override)
        return db
    baseline = fixture('valid-v2', '2m')
    with connect(baseline) as c:
        valid_facts, before_meta = facts(c), metadata(c)
    def digest(db):
        return run(str(candidate), 'digest', '--db', str(db))
    for i, (target, invalid) in enumerate(INVALID.items()):
        db = fixture(f'invalid-{i}', '2m', (target, invalid))
        with connect(db) as c:
            before, meta = facts(c), metadata(c)
            differences = [(t, j, k) for t in before for j, row in enumerate(before[t]) for k, value in enumerate(row) if value != valid_facts[t][j][k]]
            assert len(differences) == 1 and differences[0][0] == target[0], differences
            violations = []
            for (t, col), values in DOMAINS.items():
                if c.execute(f'SELECT count(*) FROM {t} WHERE {col} NOT IN ({",".join("?" for _ in values)}) OR {col} IS NULL', values).fetchone()[0]:
                    violations.append((t, col))
            if c.execute('SELECT count(*) FROM egress_accounting_uncertain WHERE reserved_bytes<0').fetchone()[0]:
                violations.append(NEGATIVE)
            assert violations == [target], violations
        d = digest(db)
        result = subprocess.run([str(candidate), 'migrate'], env=dict(os.environ, TPAHUB_DATA_DIR=str(db.parent)), capture_output=True, text=True)
        assert result.returncode != 0 and 'invalid existing' in result.stderr, result
        with connect(db) as c:
            assert c.execute('SELECT version FROM tpahub_schema').fetchall() == [(2,)]
            assert facts(c) == before and metadata(c) == meta
            healthy(c)
        assert digest(db) == d
        print('PASS migrate-rejects-' + ('negative-egress-reserved-bytes' if target == NEGATIVE else 'invalid-' + NAMES[i]))
    print('PASS p1d1-invalid-migration-fixture-isolation')
    print('PASS p1d1-invalid-migration-state-preserved')
    expected = {(t, col, v) for (t, col), values in DOMAINS.items() for v in values}
    tested = set()
    for i, ((t, col), values) in enumerate(DOMAINS.items()):
        for j, v in enumerate(values):
            db = fixture(f'valid-{i}-{j}', 3, ((t, col), v))
            with connect(db) as c:
                assert c.execute(f'SELECT {col} FROM {t}').fetchall() == [(v,)]
                healthy(c)
            tested.add((t, col, v))
            print(f'PASS valid-{NAMES[i]}-{v}')
        print(f'PASS valid-{NAMES[i]}-domain')
    print(f'expected allowed values: {len(expected)}; tested allowed values: {len(tested)}; missing: {len(expected-tested)}; extra: {len(tested-expected)}')
    assert expected == tested
    print('PASS p1d1-valid-domain-coverage-complete')
    for v in [0, 1, 1048576]:
        fixture(f'bytes-{v}', 3, (NEGATIVE, v))
    try:
        fixture('negative-v3', 3, (NEGATIVE, -1))
        raise AssertionError('negative accepted')
    except sqlite3.IntegrityError as e:
        assert 'CHECK constraint failed' in str(e)
    print('PASS valid-egress-reserved-bytes-boundaries')
    fixture('provider-state', 3, (('billing_purchases', 'provider_state'), 'future_provider_state'))
    print('PASS provider-state-remains-structurally-extensible')
    print('PASS provider-environment-source-product-identifiers-extensible')
    run(str(candidate), 'migrate', env=dict(os.environ, TPAHUB_DATA_DIR=str(baseline.parent)))
    with connect(baseline) as c:
        healthy(c)
        after_meta, after = metadata(c), facts(c)
        assert after.pop('tpahub_schema') == [(1, 3)]
        before = dict(valid_facts)
        before.pop('tpahub_schema')
        assert before == after, {t: (before[t], after[t]) for t in before if before[t] != after[t]}
    checks = {}
    for (t, col), values in DOMAINS.items():
        checks.setdefault(t, []).append(normalize(f'CHECK({col} IN ({",".join(repr(v) for v in values)}))'))
    checks.setdefault(NEGATIVE[0], []).append('check(reserved_bytes>=0)')
    with connect(work / 'empty-v3' / 'tpahub.db') as c:
        fresh_meta = metadata(c)
    for field in ['triggers', 'indexes', 'fks']:
        print(f'pre {field} set: ' + repr({t: m[field] for t, m in before_meta.items() if m[field]}))
        print(f'post {field} set: ' + repr({t: m[field] for t, m in after_meta.items() if m[field]}))
    for t in before_meta:
        for field in ['columns', 'fks', 'indexes', 'triggers']:
            assert before_meta[t][field] == after_meta[t][field], (t, field, before_meta[t][field], after_meta[t][field])
        for schema in [after_meta, fresh_meta]:
            sql = schema[t]['sql']
            for check in checks.get(t, []):
                assert sql.count(check) == 1, (t, check, sql)
                sql = sql.replace(check, '')
            assert sql == before_meta[t]['sql'], (t, sql, before_meta[t]['sql'])
    assert sum(map(len, checks.values())) == 11
    for field in ['triggers', 'indexes', 'fks']:
        print(f'{field} semantic differences: NONE')
    for name in ['p1d1-schema-scope-exact', 'migration-preserves-trigger-set', 'migration-preserves-index-set', 'migration-preserves-foreign-key-definitions', 'migration-only-adds-p1d1-constraints', 'p1d1-production-fixture-business-facts-preserved']:
        print('PASS ' + name)
    for directory in [work / 'empty-v3', l4b[0].parent]:
        db = directory / 'tpahub.db'
        d = digest(db)
        with connect(db) as c:
            m = metadata(c)
        start(candidate, directory, 3)
        assert digest(db) == d, f'startup mutated {directory}'
        with connect(db) as c:
            assert metadata(c) == m
    print('PASS current-schema-startup-no-op')
    print('fixture provider_state vocabulary: open, future_provider_state')


if __name__ == '__main__':
    with tempfile.TemporaryDirectory(prefix='tpahub-q-a-') as directory:
        main(Path(directory))
