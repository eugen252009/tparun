CREATE TABLE account_metadata(account_id TEXT PRIMARY KEY REFERENCES accounts(id),account_class TEXT NOT NULL DEFAULT 'unclassified');

CREATE TABLE account_ssh_credentials(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,credential_type TEXT NOT NULL CHECK(credential_type='ssh'),public_key BLOB NOT NULL,fingerprint TEXT NOT NULL UNIQUE,label TEXT NOT NULL DEFAULT '',flags INTEGER NOT NULL DEFAULT 0 CHECK(flags>=0),created_at TEXT NOT NULL,revoked_at TEXT NOT NULL DEFAULT '');

CREATE TABLE account_ssh_credential_audit(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),credential_id TEXT NOT NULL,old_flags INTEGER NOT NULL CHECK(old_flags>=0),new_flags INTEGER NOT NULL CHECK(new_flags>=0),action TEXT NOT NULL,created_at TEXT NOT NULL);

CREATE TABLE account_storage_high_water(account_id TEXT NOT NULL REFERENCES accounts(id),period TEXT NOT NULL,high_water_bytes INTEGER NOT NULL CHECK(high_water_bytes >= 0),updated_at TEXT NOT NULL,PRIMARY KEY(account_id,period));

CREATE TABLE accounts(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('active','suspended')),created_at TEXT NOT NULL,membership_expires_at TEXT DEFAULT NULL,purge_after TEXT DEFAULT NULL,lifecycle_kind TEXT NOT NULL DEFAULT 'customer' CHECK(lifecycle_kind IN ('customer','ephemeral','internal')));

CREATE TABLE admin_audit_log(id TEXT PRIMARY KEY,action TEXT NOT NULL,marker_id TEXT NOT NULL,account_id TEXT NOT NULL,repository_id TEXT NOT NULL,previous_state TEXT NOT NULL,previous_version INTEGER NOT NULL,result TEXT NOT NULL,operator TEXT NOT NULL,created_at TEXT NOT NULL,details TEXT NOT NULL DEFAULT '');

CREATE TABLE billing_provider_accounts(account_id TEXT NOT NULL REFERENCES accounts(id),provider TEXT NOT NULL,environment TEXT NOT NULL,provider_customer_id TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(account_id,provider,environment));

CREATE TABLE billing_provider_events(provider TEXT NOT NULL,environment TEXT NOT NULL,provider_event_id TEXT NOT NULL,event_type TEXT NOT NULL,received_at TEXT NOT NULL,processed_at TEXT NOT NULL DEFAULT '',status TEXT NOT NULL CHECK(status IN ('processing','processed','reconciliation_required')),error TEXT NOT NULL DEFAULT '',purchase_id TEXT NOT NULL DEFAULT '',provider_reference TEXT NOT NULL DEFAULT '',PRIMARY KEY(provider,environment,provider_event_id));

CREATE TABLE billing_provider_submissions(account_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,period TEXT NOT NULL,provider TEXT NOT NULL,environment TEXT NOT NULL,finalization_id TEXT NOT NULL,status TEXT NOT NULL,provider_reference TEXT NOT NULL DEFAULT '',attempted_at TEXT NOT NULL,completed_at TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',PRIMARY KEY(account_id,product_id,period,provider,environment));

CREATE TABLE billing_purchases(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,plan_code TEXT NOT NULL,offer_version TEXT NOT NULL DEFAULT 'unversioned-v1',rate_card_id TEXT NOT NULL,rate_card_mode TEXT NOT NULL,tax_classification TEXT NOT NULL DEFAULT 'saas-personal',currency TEXT NOT NULL,amount_minor INTEGER NOT NULL CHECK(amount_minor >= 0),billing_environment TEXT NOT NULL,provider TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('pending','superseded','paid','failed','expired','reconciliation_required')),entitlement_period_start TEXT NOT NULL,entitlement_period_end TEXT NOT NULL,provider_reference TEXT NOT NULL DEFAULT '',checkout_url TEXT NOT NULL DEFAULT '',checkout_request_version TEXT NOT NULL DEFAULT 'v1',provider_state TEXT NOT NULL DEFAULT 'open',reconciliation_reason TEXT NOT NULL DEFAULT '',snapshot_finalized INTEGER NOT NULL DEFAULT 0 CHECK(snapshot_finalized IN (0,1)),created_at TEXT NOT NULL,updated_at TEXT NOT NULL,paid_at TEXT NOT NULL DEFAULT '',failed_at TEXT NOT NULL DEFAULT '',expired_at TEXT NOT NULL DEFAULT '');

CREATE TABLE egress_accounting_uncertain(id TEXT PRIMARY KEY,repository_id TEXT NOT NULL REFERENCES repositories(id),created_at TEXT NOT NULL,reason TEXT NOT NULL DEFAULT '',state TEXT NOT NULL DEFAULT 'uncertain' CHECK(state IN ('streaming','uncertain')),version INTEGER NOT NULL DEFAULT 1,period_start TEXT NOT NULL DEFAULT '',period_end TEXT NOT NULL DEFAULT '',reserved_bytes INTEGER NOT NULL DEFAULT 0 CHECK(reserved_bytes>=0));

CREATE TABLE generation_cleanup_tasks(repository_id TEXT PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,attempts INTEGER NOT NULL DEFAULT 0,next_retry TEXT NOT NULL,last_error TEXT NOT NULL,updated_at TEXT NOT NULL);

CREATE TABLE global_bootstrap_repository(singleton INTEGER PRIMARY KEY CHECK(singleton=1),repository_id TEXT NOT NULL UNIQUE REFERENCES repositories(id),account_id TEXT NOT NULL REFERENCES accounts(id),created_at TEXT NOT NULL);

CREATE TABLE mock_billing_events(id TEXT NOT NULL,account_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,period TEXT NOT NULL,event_type TEXT NOT NULL,payload TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(product_id,id),UNIQUE(account_id,product_id,period,event_type));

CREATE TABLE operator_credentials(credential_id BLOB PRIMARY KEY,credential_data BLOB NOT NULL,label TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,last_used_at TEXT NOT NULL DEFAULT '');

CREATE TABLE operator_identity(id INTEGER PRIMARY KEY CHECK(id=1),user_handle BLOB NOT NULL UNIQUE);

CREATE TABLE operator_recovery(id INTEGER PRIMARY KEY CHECK(id=1),token_hash BLOB NOT NULL,created_at TEXT NOT NULL,expires_at TEXT NOT NULL,expires_at_unix_nanos INTEGER NOT NULL DEFAULT 0);

CREATE TABLE product_grants(customer_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,capability_id TEXT NOT NULL,offer_id TEXT NOT NULL,enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),valid_from TEXT NOT NULL,valid_until TEXT NOT NULL DEFAULT '',period_cadence TEXT NOT NULL CHECK(period_cadence IN ('annual','monthly','continuous')),storage_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(storage_limit_bytes>=0),egress_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(egress_limit_bytes>=0),operation_metric TEXT NOT NULL DEFAULT '',operation_limit INTEGER NOT NULL DEFAULT 0 CHECK(operation_limit>=0),source TEXT NOT NULL CHECK(source IN ('local','administrative')),purchase_id TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(customer_id,product_id,capability_id,source));

CREATE TABLE publish_jobs(id TEXT PRIMARY KEY,repository_id TEXT NOT NULL REFERENCES repositories(id),state TEXT NOT NULL CHECK(state IN ('queued','running','succeeded','failed')),incoming_path TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,started_at TEXT,finished_at TEXT,cleanup_state TEXT NOT NULL DEFAULT 'complete' CHECK(cleanup_state IN ('complete','pending')),cleanup_error TEXT NOT NULL DEFAULT '',cleanup_attempts INTEGER NOT NULL DEFAULT 0,cleanup_next_retry TEXT NOT NULL DEFAULT '',generation_id TEXT NOT NULL DEFAULT '',verification_state TEXT NOT NULL DEFAULT 'pending' CHECK(verification_state IN ('pending','match','mismatch')),verified_at TEXT NOT NULL DEFAULT '',verification_error TEXT NOT NULL DEFAULT '',publisher_account_id TEXT NOT NULL DEFAULT '');

CREATE TABLE publication_artifacts(job_id TEXT NOT NULL REFERENCES publish_jobs(id) ON DELETE CASCADE,filename TEXT NOT NULL,package TEXT NOT NULL,version TEXT NOT NULL,architecture TEXT NOT NULL,uploaded_size INTEGER NOT NULL CHECK(uploaded_size>=0),uploaded_sha256 TEXT NOT NULL CHECK(length(uploaded_sha256)=64),published_filename TEXT NOT NULL DEFAULT '',published_size INTEGER NOT NULL DEFAULT 0 CHECK(published_size>=0),published_sha256 TEXT NOT NULL DEFAULT '',verification_state TEXT NOT NULL DEFAULT 'pending' CHECK(verification_state IN ('pending','match','mismatch')),PRIMARY KEY(job_id,filename));

CREATE TABLE purchase_grant_instances(customer_id TEXT NOT NULL REFERENCES accounts(id),product_id TEXT NOT NULL,capability_id TEXT NOT NULL,purchase_id TEXT NOT NULL REFERENCES billing_purchases(id),offer_id TEXT NOT NULL,enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),valid_from TEXT NOT NULL,valid_until TEXT NOT NULL,period_cadence TEXT NOT NULL CHECK(period_cadence IN ('annual','monthly','continuous')),storage_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(storage_limit_bytes>=0),egress_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(egress_limit_bytes>=0),operation_metric TEXT NOT NULL DEFAULT '',operation_limit INTEGER NOT NULL DEFAULT 0 CHECK(operation_limit>=0),created_at TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(purchase_id,capability_id));

CREATE TABLE purchase_grant_snapshot_sets(purchase_id TEXT PRIMARY KEY REFERENCES billing_purchases(id),expected_count INTEGER NOT NULL CHECK(expected_count>0),capability_digest TEXT NOT NULL);

CREATE TABLE purchase_grant_snapshots(purchase_id TEXT NOT NULL REFERENCES billing_purchases(id),capability_id TEXT NOT NULL,period_cadence TEXT NOT NULL CHECK(period_cadence IN ('annual','monthly','continuous')),storage_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(storage_limit_bytes>=0),egress_limit_bytes INTEGER NOT NULL DEFAULT 0 CHECK(egress_limit_bytes>=0),operation_metric TEXT NOT NULL DEFAULT '',operation_limit INTEGER NOT NULL DEFAULT 0 CHECK(operation_limit>=0),PRIMARY KEY(purchase_id,capability_id));

CREATE TABLE reader_credentials(id TEXT PRIMARY KEY,repository_id TEXT NOT NULL REFERENCES repositories(id),name TEXT NOT NULL,token_prefix TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,revoked_at TEXT,UNIQUE(repository_id,name));

CREATE TABLE repositories(id TEXT PRIMARY KEY,account_id TEXT NOT NULL, name TEXT NOT NULL,visibility TEXT NOT NULL DEFAULT 'public' CHECK(visibility IN ('public','private')),created_at TEXT NOT NULL,UNIQUE(account_id,name),FOREIGN KEY(account_id) REFERENCES accounts(id));

CREATE TABLE repository_delegations(account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,repository_id TEXT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,capability_id TEXT NOT NULL CHECK(capability_id IN ('repository.publish')),enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),operator TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(account_id,repository_id,capability_id));

CREATE TABLE repository_egress_daily(repository_id TEXT NOT NULL REFERENCES repositories(id),usage_date TEXT NOT NULL,egress_bytes INTEGER NOT NULL CHECK(egress_bytes >= 0),updated_at TEXT NOT NULL,PRIMARY KEY(repository_id,usage_date));

CREATE TABLE repository_egress_usage(repository_id TEXT NOT NULL REFERENCES repositories(id),period TEXT NOT NULL,egress_bytes INTEGER NOT NULL CHECK(egress_bytes >= 0),updated_at TEXT NOT NULL,PRIMARY KEY(repository_id,period));

CREATE TABLE repository_publications(repository_id TEXT PRIMARY KEY REFERENCES repositories(id),active_generation TEXT NOT NULL,updated_at TEXT NOT NULL);

CREATE TABLE sessions(id TEXT PRIMARY KEY,account_id TEXT NOT NULL REFERENCES accounts(id),token_prefix TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,last_used_at TEXT,revoked_at TEXT,credential_id TEXT NOT NULL DEFAULT '');

CREATE TABLE tpahub_schema(singleton INTEGER PRIMARY KEY CHECK(singleton=1),version INTEGER NOT NULL CHECK(version > 0));

CREATE UNIQUE INDEX billing_purchases_pending ON billing_purchases(account_id,product_id,provider,billing_environment) WHERE status='pending';

CREATE UNIQUE INDEX billing_purchases_provider_reference ON billing_purchases(provider, billing_environment, provider_reference) WHERE provider_reference<>'';

CREATE INDEX publish_jobs_queue ON publish_jobs(state,created_at);

CREATE INDEX purchase_grant_instances_lookup ON purchase_grant_instances(customer_id,product_id,capability_id);

CREATE INDEX repository_delegations_repository ON repository_delegations(repository_id,capability_id,enabled);

CREATE INDEX repository_egress_daily_date ON repository_egress_daily(usage_date);

CREATE INDEX repository_egress_period ON repository_egress_usage(period);

CREATE TRIGGER billing_purchase_owner_immutable BEFORE UPDATE ON billing_purchases WHEN (OLD.snapshot_finalized=1 OR OLD.status='reconciliation_required') AND NEW.account_id<>OLD.account_id BEGIN SELECT RAISE(ABORT,'billing purchase ownership is immutable'); END;

CREATE TRIGGER billing_purchase_reconciliation_reason_immutable BEFORE UPDATE ON billing_purchases WHEN OLD.status='reconciliation_required' AND NEW.reconciliation_reason<>OLD.reconciliation_reason BEGIN SELECT RAISE(ABORT,'billing purchase reconciliation reason is immutable'); END;

CREATE TRIGGER billing_purchase_terms_immutable AFTER UPDATE ON billing_purchases WHEN (OLD.snapshot_finalized=1 OR OLD.status='reconciliation_required') AND (NEW.snapshot_finalized<>OLD.snapshot_finalized OR NEW.plan_code<>OLD.plan_code OR NEW.offer_version<>OLD.offer_version OR NEW.product_id<>OLD.product_id OR NEW.amount_minor<>OLD.amount_minor OR NEW.currency<>OLD.currency OR NEW.rate_card_id<>OLD.rate_card_id OR NEW.rate_card_mode<>OLD.rate_card_mode OR NEW.tax_classification<>OLD.tax_classification OR NEW.entitlement_period_start<>OLD.entitlement_period_start OR NEW.entitlement_period_end<>OLD.entitlement_period_end OR NEW.provider<>OLD.provider OR NEW.provider_reference<>OLD.provider_reference OR NEW.billing_environment<>OLD.billing_environment) BEGIN SELECT RAISE(ABORT,'billing purchase terms are immutable'); END;

CREATE TRIGGER purchase_grant_instances_immutable_delete BEFORE DELETE ON purchase_grant_instances BEGIN SELECT RAISE(ABORT,'purchase grant instances are immutable'); END;

CREATE TRIGGER purchase_grant_instances_immutable_update BEFORE UPDATE ON purchase_grant_instances BEGIN SELECT RAISE(ABORT,'purchase grant instances are immutable'); END;

CREATE TRIGGER purchase_grant_instances_no_unauthorized_insert BEFORE INSERT ON purchase_grant_instances BEGIN SELECT CASE WHEN EXISTS(SELECT 1 FROM billing_purchases p WHERE p.id=NEW.purchase_id AND p.status='paid') THEN RAISE(ABORT,'purchase grant instances are immutable after activation') END; END;

CREATE TRIGGER purchase_grant_snapshot_sets_immutable_delete BEFORE DELETE ON purchase_grant_snapshot_sets BEGIN SELECT RAISE(ABORT,'purchase grant snapshot sets are immutable'); END;

CREATE TRIGGER purchase_grant_snapshot_sets_immutable_update BEFORE UPDATE ON purchase_grant_snapshot_sets BEGIN SELECT RAISE(ABORT,'purchase grant snapshot sets are immutable'); END;

CREATE TRIGGER purchase_grant_snapshot_sets_no_late_insert BEFORE INSERT ON purchase_grant_snapshot_sets WHEN EXISTS(SELECT 1 FROM billing_purchases WHERE id=NEW.purchase_id AND (snapshot_finalized=1 OR status='reconciliation_required')) BEGIN SELECT RAISE(ABORT,'purchase grant snapshot sets are finalized'); END;

CREATE TRIGGER purchase_grant_snapshots_immutable_delete BEFORE DELETE ON purchase_grant_snapshots BEGIN SELECT RAISE(ABORT,'purchase grant snapshots are immutable'); END;

CREATE TRIGGER purchase_grant_snapshots_immutable_update BEFORE UPDATE ON purchase_grant_snapshots BEGIN SELECT RAISE(ABORT,'purchase grant snapshots are immutable'); END;

CREATE TRIGGER purchase_grant_snapshots_no_late_insert BEFORE INSERT ON purchase_grant_snapshots WHEN EXISTS(SELECT 1 FROM billing_purchases WHERE id=NEW.purchase_id AND (snapshot_finalized=1 OR status='reconciliation_required')) BEGIN SELECT RAISE(ABORT,'purchase grant snapshots are finalized'); END;

CREATE TRIGGER product_grants_identity_immutable BEFORE UPDATE ON product_grants WHEN NEW.customer_id<>OLD.customer_id OR NEW.product_id<>OLD.product_id OR NEW.capability_id<>OLD.capability_id OR NEW.source<>OLD.source BEGIN SELECT RAISE(ABORT,'grant identity is immutable'); END;


