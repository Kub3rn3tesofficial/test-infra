#standardSQL
select
  job,
  build_consistency,
  commit_consistency,
  flakes,
  runs,
  commits,
  array(
    select as struct
      i.n name,
      count(i.failures) flakes
    from tt.tests i
    group by name
    having name not in ('Test', 'DiffResources')  /* uninteresting tests */
    order by flakes desc
    limit 3 /* top three flakiest tests in this job */
  ) flakiest
from (
  select
    job, /* name of job */
    round(sum(if(flaked=1,passed,runs))/sum(runs),3) build_consistency, /* percentage of runs that flaked */
    round(1-sum(flaked)/sum(runs),3) commit_consistency, /* percentage of commits that flaked */
    sum(flaked) flakes, /* number of times it flaked */
    sum(runs) runs, /* number of times the job ran */
    count(distinct commit) commits, /* number of commits tested */
    array_concat_agg(tests) tests /* array of flaking tests in this job */
  from (
    select
      job,
      stamp,  /* TODO(fejta): use or remove */
      num,
      commit,
      if(passed = runs or passed = 0, 0, 1) flaked, /* consistent: always pass or always fail */
      passed,
      safe_cast(runs as int64) runs,
      array(
        select as struct
          i.name n, /* test name */
          countif(i.failed) failures /* number of times it flaked */
        from tt.tests i
        group by n
        having failures > 0 and failures < tt.runs /* same consistency metric */
        order by failures desc
      ) tests
    from (
      select
        job,
        num, /* pr number or null for ci */
        if(kind = 'pull', commit, version) commit, /* version for build, in metadata for ci */
        max(stamp) stamp,  /* most recent run of this job on this commit */
        sum(if(result='SUCCESS',1,0)) passed,
        count(result) runs,  /* count the number of times we ran a job on this commit for this PR */
        array_concat_agg(test) tests /* create an array of tests structs */
      from (
        SELECT
          job,
          regexp_extract(path, r'pull/(\d+)') as num,  /* pr number */
          if(substr(job, 0, 3) = 'pr:', 'pull', 'ci') kind,  /* pull or ci */
          version, /* bootstrap git version, empty for ci  */
          regexp_extract(
            (
              select i.value
              from t.metadata i
              where i.key = 'repos'
            ),
            r'[^,]+,\d+:([a-f0-9]+)"') commit,  /* repo commit, filled by ci */
          date(started) stamp,
          date_trunc(date(started), week) wk,  /* TODO(fejta): use or delete */
          result,  /* SUCCESS if the build passed */
          test  /* repeated tuple of tests */
        FROM `k8s-gubernator.build.week` as t
        where
          datetime(started) > datetime_sub(current_datetime(), interval 7 DAY)
          and version != 'unknown'
          and (
            exists(
              select as struct
                i
              from t.metadata i
              where i.key = 'repos'
            )
            or substr(job, 0, 3) = 'ci-'
        )
      )
      group by job, num, commit /* need both commit and num (batched merges and duplicate PRs) */
    ) as tt
  ) as tt
  group by job /* summarize info for this job across all commits/builds */
  order by flakes desc, build_consistency, commit_consistency, job
) as tt
order by flakes desc, build_consistency, commit_consistency, job /* flakiest jobs first */
