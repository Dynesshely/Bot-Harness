-- ============================================================================
-- pg-timestamptz-verify.sql — 校验 TEXT -> timestamptz 迁移结果
-- ============================================================================
--
-- 【执行方式】必须跟迁移脚本在【同一个 psql 会话】里执行：
--
--     docker exec -it bot-harness-postgres psql -U bot -d bot \
--       -f /tmp/pg-timestamptz.sql -f /tmp/pg-timestamptz-verify.sql
--
--   原因：③④ 要拿「迁移前的原文」和「迁移后的时刻」对照，而迁移后原文已经读不出来
--   （列已变成 timestamptz）。迁移脚本因此在同一会话内把原文快照进 TEMP 表
--   snap_<表>；会话一断，快照消失，本脚本只能做 ① 类型检查，其余标记 SKIP。
--   单独执行本脚本不会报错 —— 它会明确告诉你哪些检查被跳过了。
--
-- 【检查项】（结果表 ts_check 的 kind 列）
--   ① type      information_schema.columns.data_type 是否已是 timestamp with time zone
--               （逐列 18 项，含「列缺失」判定）
--   ② rowcount  每表行数是否与迁移前快照一致
--   ③ instant   每个非空列：迁移前后【时刻】是否严格一致
--               extract(epoch from 新值) vs extract(epoch from 迁移前文本::timestamptz)
--               比较用 IS DISTINCT FROM，NULL 也算不一致 —— 覆盖丢行/多行。
--   ③ text      新值渲染回 RFC3339 是否与迁移前原文逐字一致：
--                 to_char(col, 'YYYY-MM-DD"T"HH24:MI:SSOF')  （会话时区 Asia/Shanghai）
--               对照 regexp_replace(原文, '([+-][0-9]{2}):([0-9]{2})$', '\1')
--               （原文 +08:00 -> +08，因为 OF 只打 2 位小时偏移）
--   ④ nullcount 可空列：原文空串数 == 迁移后 NULL 数（聚合口径）
--     nullmap   可空列：逐行 (新值 IS NULL) 是否等价于 (原文 = '')（行级口径）
--   ①b nullability 那 5 个可空列必须 is_nullable = YES（旧 DDL 是 NOT NULL），
--        其余 13 列必须保持 NO
--      default     那 5 个列的 DEFAULT '' 必须已删除（否则新代码不写该列会拿到 '' -> 报错）
--        （nullability / default 是结构检查，不依赖会话快照，单跑本脚本也会查）
--   ⑤ summary   每表 OK/FAIL 汇总 + 总计
--
--   ⚠ 关于 ③ text 的写法：任务给的表达式是
--       to_char(col AT TIME ZONE 'Asia/Shanghai', 'YYYY-MM-DD"T"HH24:MI:SSOF')
--     AT TIME ZONE 的结果是 timestamp *without* time zone，而 to_char 的 OF 只对
--     timestamptz 有意义；对无时区 timestamp，OF 打出来的是假偏移 "+00"（已实测：
--     19:05+08 渲染成 2026-11-01T19:05:00+00），跟原文 "+08:00" 永远不相等。
--     所以这里改用 SET LOCAL TimeZone='Asia/Shanghai' + to_char(timestamptz, ...OF)，
--     语义等价且偏移正确："2026-11-01T19:05:00+08"。
--     若历史数据里有非 +08:00 的原文，text 会 FAIL 而 instant 仍 OK —— 那是提示
--     原文时区不统一，时刻本身没错：迁移正确性以 instant 项为准。
--
-- 【结论口径】
--   任何一行 bad > 0 就是 FAIL，脚本最后会 RAISE EXCEPTION 让 psql 以非 0 退出
--   （可直接用于演练/CI 判定）；全 OK 则正常 COMMIT。
-- ============================================================================

\set ON_ERROR_STOP on

\echo '== pg-timestamptz-verify: 校验开始 =='

-- 结果表与期望清单必须在只读事务【之前】建好：READ ONLY 事务里 CREATE/DROP TABLE
-- 会被拒绝（连临时表也不行；对已存在的临时表 INSERT 才是允许的）。
DROP TABLE IF EXISTS pg_temp.ts_check;
CREATE TEMP TABLE ts_check (
  tbl  text  NOT NULL,
  col  text,
  kind text  NOT NULL,
  bad  bigint NOT NULL,
  note text
);

-- 期望的时间列清单（必须与 scripts/pg-timestamptz.sql 的转换清单一致）。
-- nullable=true 表示旧约定用 '' 表示未设置、且原来带 DEFAULT ''。
DROP TABLE IF EXISTS pg_temp.ts_expected;
CREATE TEMP TABLE ts_expected (
  tbl      text    NOT NULL,
  col      text    NOT NULL,
  nullable boolean NOT NULL
);

INSERT INTO ts_expected(tbl, col, nullable) VALUES
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
  ('stats',            'bucket',          false);

-- 可重复读 + 只读：② 的行数对照不会被并发写入扰动（切换瞬间本来也已停服）。
-- 事务内只做 SELECT / INSERT 到上面已建好的临时表。
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;

-- ③ text 的渲染基准：固定成线上数据的本地时区。
SET LOCAL TimeZone = 'Asia/Shanghai';

-- ---------------------------------------------------------------------------
-- ① 类型
-- ---------------------------------------------------------------------------
INSERT INTO ts_check(tbl, col, kind, bad, note)
SELECT e.tbl, e.col, 'type',
       CASE WHEN c.data_type = 'timestamp with time zone' THEN 0 ELSE 1 END,
       coalesce(c.data_type, 'MISSING COLUMN')
  FROM ts_expected e
  LEFT JOIN information_schema.columns c
         ON c.table_schema = 'public'
        AND c.table_name   = e.tbl
        AND c.column_name  = e.col;

-- ---------------------------------------------------------------------------
-- ①b 可空列必须真的是「可空」：旧 DDL 是 NOT NULL DEFAULT ''，迁移会
--     DROP NOT NULL + DROP DEFAULT。这两项不依赖会话快照，任何时候都查。
-- ---------------------------------------------------------------------------
-- 那 5 列必须变 YES（旧 DDL 是 NOT NULL）；其余 13 列必须保持 NO。
INSERT INTO ts_check(tbl, col, kind, bad, note)
SELECT e.tbl, e.col, 'nullability',
       CASE WHEN (c.is_nullable = 'YES') = e.nullable THEN 0 ELSE 1 END,
       coalesce('is_nullable = ' || c.is_nullable, 'MISSING COLUMN')
         || CASE WHEN e.nullable THEN '（期望 YES）' ELSE '（期望 NO）' END
  FROM ts_expected e
  LEFT JOIN information_schema.columns c
         ON c.table_schema = 'public'
        AND c.table_name   = e.tbl
        AND c.column_name  = e.col;

INSERT INTO ts_check(tbl, col, kind, bad, note)
SELECT e.tbl, e.col, 'default',
       CASE WHEN c.column_default IS NULL THEN 0 ELSE 1 END,
       coalesce('DEFAULT 仍存在：' || c.column_default, 'DEFAULT 已删除')
  FROM ts_expected e
  LEFT JOIN information_schema.columns c
         ON c.table_schema = 'public'
        AND c.table_name   = e.tbl
        AND c.column_name  = e.col
 WHERE e.nullable;

-- ---------------------------------------------------------------------------
-- ②③④ 需要迁移会话留下的 TEMP 台账：ts_rowcount / ts_migrated / snap_<表>
-- ---------------------------------------------------------------------------
DO $verify$
DECLARE
  e          record;
  t          record;
  pk_col     text;
  pk_type    text;
  old_ref    text;
  join_expr  text;
  inst_expr  text;
  text_expr  text;
  n_base     bigint;
  n_now      bigint;
  n_total    bigint;
  n_bad      bigint;
  n_conv     bigint;
  n_old_empty bigint;
  n_new_null bigint;
BEGIN
  -- ---------------- ② 行数 ----------------
  IF to_regclass('pg_temp.ts_rowcount') IS NULL THEN
    INSERT INTO ts_check(tbl, col, kind, bad, note)
    SELECT DISTINCT tbl, NULL, 'rowcount', 0,
           'SKIP：不在迁移会话内（没有 ts_rowcount 台账）'
      FROM ts_expected;
  ELSE
    FOR t IN SELECT DISTINCT tbl FROM ts_expected ORDER BY tbl LOOP
      EXECUTE 'SELECT n FROM pg_temp.ts_rowcount WHERE tbl = $1' INTO n_base USING t.tbl;
      EXECUTE format('SELECT count(*) FROM %I', t.tbl) INTO n_now;
      INSERT INTO ts_check(tbl, col, kind, bad, note)
      VALUES (t.tbl, NULL, 'rowcount',
              CASE WHEN n_base IS NULL THEN 1
                   WHEN n_now = n_base   THEN 0
                   ELSE 1 END,
              format('迁移前 %s / 现在 %s',
                     coalesce(n_base::text, '?'), n_now));
    END LOOP;
  END IF;

  -- ---------------- ③④ 逐列对照 ----------------
  FOR e IN SELECT * FROM ts_expected ORDER BY tbl, col LOOP
    IF to_regclass('pg_temp.ts_migrated') IS NULL THEN
      INSERT INTO ts_check(tbl, col, kind, bad, note)
      VALUES (e.tbl, e.col, 'skipped', 0,
              'SKIP：未在同一 psql 会话内运行迁移脚本，无法对照原文');
      CONTINUE;
    END IF;

    EXECUTE 'SELECT count(*) FROM pg_temp.ts_migrated WHERE tbl = $1 AND col = $2'
       INTO n_conv USING e.tbl, e.col;
    IF n_conv = 0 THEN
      INSERT INTO ts_check(tbl, col, kind, bad, note)
      VALUES (e.tbl, e.col, 'skipped', 0,
              'SKIP：本会话没有转换这一列（运行前已是 timestamptz，重复执行属正常）');
      CONTINUE;
    END IF;

    IF to_regclass('pg_temp.snap_' || e.tbl) IS NULL THEN
      INSERT INTO ts_check(tbl, col, kind, bad, note)
      VALUES (e.tbl, e.col, 'skipped', 0, 'SKIP：缺少 snap_ 快照表');
      CONTINUE;
    END IF;

    -- 主键（12 张表都是单列主键）
    SELECT a.attname
      INTO pk_col
      FROM pg_index i
      JOIN pg_attribute a
        ON a.attrelid = i.indrelid
       AND a.attnum   = ANY (i.indkey::smallint[])
     WHERE i.indrelid = format('public.%I', e.tbl)::regclass
       AND i.indisprimary
     LIMIT 1;

    IF pk_col IS NULL THEN
      RAISE EXCEPTION '表 % 没有单列主键，无法校验时刻一致性', e.tbl;
    END IF;

    SELECT format_type(a.atttypid, a.atttypmod)
      INTO pk_type
      FROM pg_attribute a
     WHERE a.attrelid = format('public.%I', e.tbl)::regclass
       AND a.attname  = pk_col
       AND NOT a.attisdropped;

    old_ref   := format('s.%I', 'old_' || e.col);
    -- 主键可能是 text / bigint / （stats）timestamptz：快照里一律存成 text，
    -- 这里按主键【当前】的类型转回去再 JOIN。
    join_expr := format('t.%I = s.pk::%s', pk_col, pk_type);

    IF e.nullable THEN
      inst_expr := format(
        's.pk IS NULL OR (CASE WHEN %s = %L THEN t.%I IS NOT NULL '
        'ELSE t.%I IS NULL OR extract(epoch FROM t.%I) IS DISTINCT FROM extract(epoch FROM %s::timestamptz) END)',
        old_ref, '', e.col, e.col, e.col, old_ref);
    ELSE
      inst_expr := format(
        'extract(epoch FROM t.%I) IS DISTINCT FROM extract(epoch FROM %s::timestamptz)',
        e.col, old_ref);
    END IF;

    text_expr := format(
      'to_char(t.%I, %L) IS DISTINCT FROM regexp_replace(%s, %L, %L)',
      e.col, 'YYYY-MM-DD"T"HH24:MI:SSOF',
      old_ref, '([+-][0-9]{2}):([0-9]{2})$', '\1');
    IF e.nullable THEN
      text_expr := format(
        's.pk IS NULL OR (CASE WHEN %s = %L THEN t.%I IS NOT NULL '
        'ELSE t.%I IS NULL OR %s END)',
        old_ref, '', e.col, e.col, text_expr);
    END IF;

    -- ③ 时刻一致
    EXECUTE format(
      'SELECT count(*), count(*) FILTER (WHERE %s) FROM %I t FULL JOIN %I s ON %s',
      inst_expr, e.tbl, 'snap_' || e.tbl, join_expr)
      INTO n_total, n_bad;
    INSERT INTO ts_check(tbl, col, kind, bad, note)
    VALUES (e.tbl, e.col, 'instant', n_bad,
            format('对照 %s 行（快照 ∪ 现表），不一致 %s 行', n_total, n_bad));

    -- ③ 渲染文本一致
    EXECUTE format(
      'SELECT count(*), count(*) FILTER (WHERE %s) FROM %I t FULL JOIN %I s ON %s',
      text_expr, e.tbl, 'snap_' || e.tbl, join_expr)
      INTO n_total, n_bad;
    INSERT INTO ts_check(tbl, col, kind, bad, note)
    VALUES (e.tbl, e.col, 'text', n_bad,
            format('对照 %s 行，渲染不一致 %s 行', n_total, n_bad));

    IF e.nullable THEN
      -- ④ 空串数 == NULL 数（聚合）
      EXECUTE format('SELECT count(*) FROM %I WHERE %I = %L',
                     'snap_' || e.tbl, 'old_' || e.col, '') INTO n_old_empty;
      EXECUTE format('SELECT count(*) FROM %I WHERE %I IS NULL', e.tbl, e.col)
         INTO n_new_null;
      INSERT INTO ts_check(tbl, col, kind, bad, note)
      VALUES (e.tbl, e.col, 'nullcount',
              CASE WHEN n_old_empty = n_new_null THEN 0 ELSE 1 END,
              format('迁移前空串 %s 行 / 现在 NULL %s 行', n_old_empty, n_new_null));

      -- ④ 逐行：新值 IS NULL 等价于 原文 = ''
      EXECUTE format(
        'SELECT count(*), count(*) FILTER (WHERE (t.%I IS NULL) IS DISTINCT FROM (%s = %L)) '
        'FROM %I t FULL JOIN %I s ON %s',
        e.col, old_ref, '', e.tbl, 'snap_' || e.tbl, join_expr)
        INTO n_total, n_bad;
      INSERT INTO ts_check(tbl, col, kind, bad, note)
      VALUES (e.tbl, e.col, 'nullmap', n_bad,
              format('对照 %s 行，映射不一致 %s 行', n_total, n_bad));

    END IF;
  END LOOP;
END
$verify$;

-- ---------------------------------------------------------------------------
-- ① 明细
-- ---------------------------------------------------------------------------
\echo ''
\echo '== ① 时间列类型（期望 timestamp with time zone）=='
SELECT tbl AS "表", col AS "列",
       CASE WHEN bad = 0 THEN 'OK' ELSE 'FAIL' END AS "结果",
       note AS "实际类型"
  FROM ts_check
 WHERE kind = 'type'
 ORDER BY tbl, col;

-- ---------------------------------------------------------------------------
-- ②③④ 明细
-- ---------------------------------------------------------------------------
\echo ''
\echo '== ②③④ 逐列明细 =='
SELECT tbl AS "表", col AS "列", kind AS "检查",
       CASE WHEN bad = 0 THEN 'OK' ELSE 'FAIL' END AS "结果",
       bad AS "不一致行数", note AS "说明"
  FROM ts_check
 WHERE kind <> 'type'
 ORDER BY tbl, col, kind;

-- ---------------------------------------------------------------------------
-- ⑤ 逐表汇总
-- ---------------------------------------------------------------------------
\echo ''
\echo '== ⑤ 逐表汇总 =='
SELECT tbl AS "表",
       count(*) FILTER (WHERE kind <> 'skipped')  AS "已检查项",
       count(*) FILTER (WHERE bad > 0)           AS "失败项",
       count(*) FILTER (WHERE kind = 'skipped')  AS "跳过项",
       CASE WHEN count(*) FILTER (WHERE bad > 0) = 0 THEN 'OK' ELSE 'FAIL' END AS "结果"
  FROM ts_check
 GROUP BY tbl
 ORDER BY tbl;

\echo ''
\echo '== 总计 =='
SELECT count(*) FILTER (WHERE kind <> 'skipped') AS "已检查项",
       count(*) FILTER (WHERE bad > 0)          AS "失败项",
       count(*) FILTER (WHERE kind = 'skipped') AS "跳过项",
       CASE WHEN count(*) FILTER (WHERE bad > 0) = 0 THEN 'ALL OK' ELSE 'FAIL' END AS "结果"
  FROM ts_check;

-- ---------------------------------------------------------------------------
-- 结论：任何不一致都让 psql 以非 0 退出（供演练/流水线判定）
-- ---------------------------------------------------------------------------
DO $guard$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM ts_check WHERE kind IN ('instant','text')) THEN
    RAISE WARNING '本次没有做任何「转换前后对照」：迁移与校验不在同一个 psql 会话内，'
                  '或迁移是重复执行（全部跳过）。③④ 未校验。';
  END IF;

  IF EXISTS (SELECT 1 FROM ts_check WHERE bad > 0) THEN
    RAISE EXCEPTION '校验未通过：存在 FAIL 行，见上方明细';
  END IF;

  RAISE NOTICE '校验全部通过';
END
$guard$;

COMMIT;

\echo '== pg-timestamptz-verify: 校验结束 =='
