-- ============================================================================
-- pg-timestamptz.sql — 一次性迁移：时间列 TEXT(RFC3339, +08:00) -> timestamptz
-- ============================================================================
--
-- 【何时执行】
--   停服窗口内的「切换瞬间」：旧进程已停、新代码尚未启动时执行本脚本，紧接着执行
--   scripts/pg-timestamptz-verify.sql。旧代码按 RFC3339 文本写库、新代码写
--   timestamptz，两者不能同时对着同一个库跑，所以必须停服执行。
--
-- 【执行方式】
--   ★ 迁移与校验必须在【同一个 psql 会话】里完成 ★
--   本脚本在会话内建立 snap_<表> 文本快照（TEMP 表），校验脚本读它做
--   「转换前后时刻一致」对照；会话一断快照即消失，校验就只剩类型检查。
--
--     docker cp scripts/pg-timestamptz.sql        bot-harness-postgres:/tmp/
--     docker cp scripts/pg-timestamptz-verify.sql bot-harness-postgres:/tmp/
--     docker exec -it bot-harness-postgres psql -U bot -d bot \
--       -f /tmp/pg-timestamptz.sql -f /tmp/pg-timestamptz-verify.sql
--
--   不要加 --single-transaction：本脚本自带 BEGIN/COMMIT（要保证全有或全无）；
--   也不要在外面再包一层事务。
--
-- 【前置条件：脚本外必须先备份（整库）】
--   推荐用仓库现成脚本（产出带 sha256 + MANIFEST 的快照目录，restore-pg.sh 直接吃这个格式）：
--     bash scripts/backup-pg.sh --keep 14
--   或临时抓一份（热备，只读，不影响线上；本脚本的演练就是这么干的）：
--     docker exec bot-harness-postgres pg_dump -U bot -Fc bot \
--       > bot-$(date -u +%Y%m%dT%H%M%SZ).dump
--   没有备份就不要执行本脚本。
--
-- 【回滚】列类型无法原地回退（没有「逆向 SQL」），回滚 = 用迁移前的备份恢复整库：
--   0) 先留好回滚镜像：切换前把当前（旧代码）镜像打上 tag，否则回退时无处可去
--        docker tag bot-harness:latest bot-harness:pre-timestamptz-<日期>
--   1) 停服： docker compose stop bot-harness
--      （不要用 docker compose stop：登记表里 bot-harness 只有 oneshot 的 up/down）
--   2) 恢复整库，二选一：
--      a. 走仓库脚本（会先校验 sha256、并在机器人容器在跑时拒绝执行）：
--           bash scripts/restore-pg.sh <快照目录> --database bot --force
--         （--force 是因为它把「容器在跑」当作危险信号；已确认停服才加）
--      b. 或彻底重建库再灌（不留任何 timestamptz 残留对象）：
--           docker exec bot-harness-postgres dropdb  -U bot --if-exists bot
--           docker exec bot-harness-postgres createdb -U bot bot
--           docker exec -i bot-harness-postgres pg_restore -U bot -d bot --no-owner < <备份文件>
--   3) 回退镜像：docker tag bot-harness:pre-timestamptz-<日期> bot-harness:latest 后
--      ( set -a; . ./.env; set +a; docker compose up -d --no-deps --force-recreate bot-harness )
--      —— 不要加 --build，否则会用当前代码重建，回滚点就没了。
--   4) 冒烟：/api/health 200；GET /api/status 的 store.driver=postgres、size_bytes 非 0；
--      后台「会话 / 消息记录」能打开；SELECT 一条旧记录确认时间列又是文本。
--   注意：恢复备份会丢掉「备份之后、停服之前」写入的行 —— 窗口越短越好。
--
-- 【风险】
--   * ALTER TYPE 是【全表重写】：持 ACCESS EXCLUSIVE 锁，期间该表读写全部阻塞，
--     表上所有索引（含主键）被重建。stats.bucket 是主键且被转换，主键索引同样
--     重建 —— 属预期。
--   * 锁：脚本设 lock_timeout=15s，拿不到锁就整笔回滚（宁可失败，也不排队挂住）。
--   * 18 列在【一个事务】里完成：要么整体 timestamptz，要么整体不变。
--   * 旧二进制 + 新 schema 会写失败（它仍按文本写），所以必须先停服。
--   * 这 5 个「可空」列在旧 DDL 里是 NOT NULL DEFAULT ''。迁移会【DROP NOT NULL
--     和 DEFAULT】把它们真正变成可空：未设置即 NULL。读侧要按 NULL 处理，写侧
--     不能再塞 ''（''::timestamptz 会直接报错）。
--   * 实测（临时库副本 = 线上快照，数据量同线上：records 12,491 / messages 1,450 /
--     stats 1,057 / dedup 672 / media 145 行但 bytea 约 44 MB 在 TOAST 里 /
--     conversations 6 / tool_calls 41 / scheduled_tasks 9 / settings 31 /
--     schema_migrations 10；整库 64 MB）：
--       - 18 列 ALTER 合计 ≈ 0.89 s（psql 逐条计时）；整笔迁移事务（快照 + 转换 +
--         ANALYZE + COMMIT）≈ 1.2 s（另一轮 1.5 s，容器调度抖动）；校验另 ≈ 0.5 s
--         （另一次 0.9 s，含临时快照表销毁）；端到端 psql 进程 1.8 s / 2.5 s。
--       - 单表最长：media 0.73 s（整表重写会把 44 MB TOAST 一并重写）；
--         records 0.10 s（含 idx_records_delivery 重建）；其余 12 张表均 < 10 ms。
--       - ANALYZE 合计约 0.14 s（media 0.12 s），COMMIT 约 15 ms。
--     线上体量同量级 => 1–2 秒级停服窗口。
--
-- 【幂等】
--   每列单独查 information_schema.columns.data_type；已是
--   timestamp with time zone 就打印 NOTICE 并跳过。重复执行不报错、不改数据。
--
-- 【列清单】18 列
--   非空(13)：conversations.created_at, last_active_at；messages.created_at；
--             records.created_at；tool_calls.created_at；memories.created_at；
--             scheduled_tasks.due_at, created_at；dedup.created_at；
--             settings.updated_at；media.created_at；schema_migrations.applied_at；
--             stats.bucket
--   可空(5) ：memories.last_used_at；proactive_state.last_sent_at, updated_at；
--             scheduled_tasks.lease_until, sent_at
--             旧 DDL 里是 NOT NULL DEFAULT ''（'' = 未设置）；迁移同时
--             DROP NOT NULL + DROP DEFAULT，与 internal/store/pg.go 的新 DDL 对齐
-- ============================================================================

\set ON_ERROR_STOP on

\echo '== pg-timestamptz: TEXT -> timestamptz 迁移开始 =='

BEGIN;

-- 会话时区固定为 Asia/Shanghai：
--   * 让万一缺 offset 的旧文本按「本地时间」解释，而不是按 UTC（RFC3339 自带
--     +08:00 时不生效，纯属兜底）；
--   * 让后面的 NOTICE / 报表按本地时间显示。
SET LOCAL TimeZone = 'Asia/Shanghai';
-- 拿不到锁就失败（整笔回滚），不要排队挂住线上。
SET LOCAL lock_timeout = '15s';
-- 全表重写期间不要被 statement_timeout 半路砍掉。
SET LOCAL statement_timeout = 0;

-- ---------------------------------------------------------------------------
-- 0. 会话内台账（TEMP，随会话消失）
-- ---------------------------------------------------------------------------
CREATE TEMP TABLE IF NOT EXISTS ts_migrated (
  tbl          text        NOT NULL,
  col          text        NOT NULL,
  converted_at timestamptz NOT NULL DEFAULT now()
);
TRUNCATE ts_migrated;

CREATE TEMP TABLE IF NOT EXISTS ts_rowcount (
  tbl text   PRIMARY KEY,
  n   bigint NOT NULL
);
TRUNCATE ts_rowcount;

-- ---------------------------------------------------------------------------
-- 1. 迁移前文本快照：snap_<表>(pk, old_<列> ...)
--    必须在任何 ALTER 之前跑。只快照【当前还是 text】的列，已经是
--    timestamptz 的列不进快照（那种情况下校验脚本会跳过该列的对照）。
-- ---------------------------------------------------------------------------
DO $snap$
DECLARE
  spec record;
  cols text;
BEGIN
  FOR spec IN
    SELECT * FROM (VALUES
      ('conversations',    'id',           ARRAY['created_at','last_active_at']),
      ('messages',         'id',           ARRAY['created_at']),
      ('records',          'id',           ARRAY['created_at']),
      ('tool_calls',       'id',           ARRAY['created_at']),
      ('memories',         'id',           ARRAY['created_at','last_used_at']),
      ('proactive_state',  'group_openid', ARRAY['last_sent_at','updated_at']),
      ('scheduled_tasks',  'id',           ARRAY['due_at','lease_until','created_at','sent_at']),
      ('dedup',            'msg_id',       ARRAY['created_at']),
      ('settings',         'k',            ARRAY['updated_at']),
      ('media',            'id',           ARRAY['created_at']),
      ('schema_migrations','name',         ARRAY['applied_at']),
      ('stats',            'bucket',       ARRAY['bucket'])
    ) AS t(tbl, pk, cols)
  LOOP
    IF to_regclass('pg_temp.snap_' || spec.tbl) IS NOT NULL THEN
      RAISE NOTICE 'snap_% 已存在，跳过（同一会话内重复执行）', spec.tbl;
      CONTINUE;
    END IF;

    SELECT string_agg(format('%I::text AS %I', u.c, 'old_' || u.c), ', ' ORDER BY u.ord)
      INTO cols
      FROM unnest(spec.cols) WITH ORDINALITY AS u(c, ord)
     WHERE EXISTS (SELECT 1
                     FROM information_schema.columns ic
                    WHERE ic.table_schema = 'public'
                      AND ic.table_name   = spec.tbl
                      AND ic.column_name  = u.c
                      AND ic.data_type    = 'text');

    IF cols IS NULL THEN
      -- 该表的时间列已全部是 timestamptz：留一张只有 pk 的空快照占位。
      EXECUTE format('CREATE TEMP TABLE %I AS SELECT %I::text AS pk FROM %I WHERE false',
                     'snap_' || spec.tbl, spec.pk, spec.tbl);
      RAISE NOTICE 'snap_%：没有待转换的 text 列，仅占位', spec.tbl;
    ELSE
      EXECUTE format('CREATE TEMP TABLE %I AS SELECT %I::text AS pk, %s FROM %I',
                     'snap_' || spec.tbl, spec.pk, cols, spec.tbl);
      RAISE NOTICE 'snap_%：已快照迁移前文本', spec.tbl;
    END IF;
  END LOOP;
END
$snap$;

-- ---------------------------------------------------------------------------
-- 2. 迁移前行数快照（校验脚本用来证明「行数不变」）
-- ---------------------------------------------------------------------------
DO $rc$
DECLARE
  spec record;
BEGIN
  FOR spec IN
    SELECT tablename
      FROM pg_tables
     WHERE schemaname = 'public'
       AND tablename IN ('conversations','messages','records','tool_calls','memories',
                         'proactive_state','scheduled_tasks','dedup','settings','media',
                         'schema_migrations','stats')
     ORDER BY tablename
  LOOP
    EXECUTE format('INSERT INTO ts_rowcount(tbl, n) SELECT %L, count(*) FROM %I',
                   spec.tablename, spec.tablename);
  END LOOP;
  RAISE NOTICE '已记录 % 张表的迁移前行数', (SELECT count(*) FROM ts_rowcount);
END
$rc$;

-- ---------------------------------------------------------------------------
-- 3. 逐列转换
--    非空列：USING <c>::timestamptz
--    可空列：DROP NOT NULL + DROP DEFAULT，再 USING NULLIF(<c>, '')::timestamptz
--            —— 两步都必须在 ALTER TYPE 之前（两处坑都已实测）：
--            * 这 5 列在旧 DDL 里其实是 NOT NULL DEFAULT ''（'' 只表示「未设置」）。
--              不先 DROP NOT NULL，NULLIF 产出的 NULL 会被约束挡下：
--              「column "lease_until" of relation "scheduled_tasks" contains
--               null values」，整笔事务回滚；
--            * DEFAULT 也必须先删：PostgreSQL 会尝试把旧默认值转成新类型，
--              ''::timestamptz 无法隐式转换：
--              「default for column ... cannot be cast automatically」。
--            迁移后这 5 列与 internal/store/pg.go 的新 DDL 完全一致：TIMESTAMPTZ
--            可空、无默认值。
-- ---------------------------------------------------------------------------
DO $mig$
DECLARE
  spec         record;
  cur_type     text;
  cur_nullable text;
  cur_def      text;
  n_empty      bigint;
  n_odd        bigint;
BEGIN
  FOR spec IN
    SELECT * FROM (VALUES
      ('conversations',    'created_at',      false),
      ('conversations',    'last_active_at',  false),
      ('messages',         'created_at',      false),
      ('records',          'created_at',      false),
      ('tool_calls',       'created_at',      false),
      ('memories',         'created_at',      false),
      ('memories',         'last_used_at',    true),
      ('proactive_state',  'last_sent_at',    true),
      ('proactive_state',  'updated_at',      true),
      ('scheduled_tasks',  'due_at',          false),
      ('scheduled_tasks',  'lease_until',     true),
      ('scheduled_tasks',  'created_at',      false),
      ('scheduled_tasks',  'sent_at',         true),
      ('dedup',            'created_at',      false),
      ('settings',         'updated_at',      false),
      ('media',            'created_at',      false),
      ('schema_migrations','applied_at',      false),
      ('stats',            'bucket',          false)
    ) AS t(tbl, col, nullable)
  LOOP
    SELECT c.data_type, c.is_nullable, c.column_default
      INTO cur_type, cur_nullable, cur_def
      FROM information_schema.columns c
     WHERE c.table_schema = 'public'
       AND c.table_name   = spec.tbl
       AND c.column_name  = spec.col;

    IF cur_type IS NULL THEN
      RAISE EXCEPTION '迁移中止：列 %.% 不存在（表结构不是预期的版本？）', spec.tbl, spec.col;
    END IF;

    -- 可空列的结构性修正先做，且幂等（已改过就是 no-op）：
    -- 这样即使上一次执行只完成了一部分（例如列已是 timestamptz 但仍是 NOT NULL），
    -- 重复执行也能自愈。
    IF spec.nullable THEN
      IF cur_nullable = 'NO' THEN
        EXECUTE format('ALTER TABLE %I ALTER COLUMN %I DROP NOT NULL', spec.tbl, spec.col);
        RAISE NOTICE '  %.%：DROP NOT NULL（旧 DDL 是 NOT NULL，空串才表示未设置）',
                     spec.tbl, spec.col;
      END IF;
      IF cur_def IS NOT NULL THEN
        EXECUTE format('ALTER TABLE %I ALTER COLUMN %I DROP DEFAULT', spec.tbl, spec.col);
        RAISE NOTICE '  %.%：DROP DEFAULT（原 %）', spec.tbl, spec.col, cur_def;
      END IF;
    END IF;

    IF cur_type = 'timestamp with time zone' THEN
      RAISE NOTICE '跳过 %.%：已是 timestamp with time zone', spec.tbl, spec.col;
      CONTINUE;
    END IF;

    IF cur_type <> 'text' THEN
      RAISE EXCEPTION '迁移中止：%.% 的类型是 %，既不是 text 也不是 timestamptz',
                      spec.tbl, spec.col, cur_type;
    END IF;

    -- 数据格式提示（只告警、不阻断）：RFC3339 里带 'Z' 或小数秒的行，::timestamptz
    -- 仍能正确解析，但校验脚本的 text 项会把它们标成「渲染不一致」（那些行的
    -- 时刻本身没问题，instant 项才是权威口径）。线上实测 18 列全部是 +HH:MM，为 0。
    EXECUTE format(
      'SELECT count(*) FROM %I WHERE %I <> %L AND %I !~ %L',
      spec.tbl, spec.col, '', spec.col,
      '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}[+-][0-9]{2}:[0-9]{2}$')
      INTO n_odd;
    IF n_odd > 0 THEN
      RAISE WARNING '数据格式提示：%.% 有 % 行不是 RFC3339(+HH:MM)，仍按 ::timestamptz 解析；'
                    '校验脚本的 text 项会把这些行标成 FAIL，时刻以 instant 项为准',
                    spec.tbl, spec.col, n_odd;
    END IF;

    IF spec.nullable THEN
      EXECUTE format('SELECT count(*) FROM %I WHERE %I = %L', spec.tbl, spec.col, '')
         INTO n_empty;
      EXECUTE format(
        'ALTER TABLE %I ALTER COLUMN %I TYPE timestamptz USING NULLIF(%I, %L)::timestamptz',
        spec.tbl, spec.col, spec.col, '');
      RAISE NOTICE '  %.%：空串 -> NULL（% 行），类型改为 timestamptz', spec.tbl, spec.col, n_empty;
    ELSE
      -- 非空列里出现 '' 会让 ::timestamptz 直接报错。先查，报出具体行数，
      -- 整笔事务回滚 —— 不要静默改写数据。
      EXECUTE format('SELECT count(*) FROM %I WHERE %I = %L', spec.tbl, spec.col, '')
         INTO n_empty;
      IF n_empty > 0 THEN
        RAISE EXCEPTION '迁移中止：非空列 %.% 有 % 行是空串，::timestamptz 会失败；请先人工处理',
                        spec.tbl, spec.col, n_empty;
      END IF;

      EXECUTE format('ALTER TABLE %I ALTER COLUMN %I TYPE timestamptz USING %I::timestamptz',
                     spec.tbl, spec.col, spec.col);
      RAISE NOTICE '  %.%：类型改为 timestamptz', spec.tbl, spec.col;
    END IF;

    INSERT INTO ts_migrated(tbl, col) VALUES (spec.tbl, spec.col);
  END LOOP;

  RAISE NOTICE '本次会话共转换 % 列（其余列原本已是 timestamptz，已跳过）',
               (SELECT count(*) FROM ts_migrated);
END
$mig$;

-- ---------------------------------------------------------------------------
-- 4. 刷新统计信息（ALTER TYPE 全表重写后，规划器统计已失效）
-- ---------------------------------------------------------------------------
ANALYZE conversations;
ANALYZE messages;
ANALYZE records;
ANALYZE tool_calls;
ANALYZE memories;
ANALYZE proactive_state;
ANALYZE scheduled_tasks;
ANALYZE dedup;
ANALYZE settings;
ANALYZE media;
ANALYZE schema_migrations;
ANALYZE stats;

COMMIT;

\echo '== 迁移已 COMMIT =='
\echo '== 下一步（同一会话、不要断开连接）：执行 scripts/pg-timestamptz-verify.sql =='
