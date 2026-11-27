"""Create a field projection transcribed from a pinned, public upstream fixture.
This script does NOT generate benchmark questions or ground-truth labels.
Upstream: tenurehq/precisionMemBench @ 61a712a1754e7d27b6dee42da32a2c214b003ece.
The original full dataset is fetched separately by the provenance-aware downloader.
"""
import json
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
seeds=[]
def b(id, name, aliases, content, scope='code', typ='preference', pinned=False, status='active', user='test-user', superseded_by=None, resolved_at=None, user_edited=False, participants=None):
    seeds.append(dict(_id='b-'+id,user_id=user,type=typ,canonical_name=name,aliases=aliases,content=content,scope=[scope if ':' in scope else 'domain:'+scope],epistemic_status=status,pinned=pinned,superseded_by=superseded_by,resolved_at=resolved_at,user_edited=user_edited,participants=participants or []))
b('comm-response-length','response_length',['verbosity','directness'],'Reads responses carefully rather than skimming. Prefers direct answers without lengthy preamble.','user:universal',pinned=True)
b('comm-pushback','challenge_mode',['pushback','disagreement'],'Wants explicit pushback when a plan has problems, rather than silent compliance.','user:universal',pinned=True)
b('comm-recommendation','recommendation_style',[],'Prefers a single strong recommendation over a menu of options.','user:universal')
b('authorship-mode','authorship_mode',[],'Edits AI output rather than having AI edit their own drafts.','user:universal')
b('ts-pref','language_typescript',['TypeScript','TS'],'Writes TypeScript with strict mode and no implicit any.')
b('fastify-pref','framework_fastify',['Fastify','express'],'Uses Fastify for HTTP services rather than Express.')
b('mongo-raw-driver','mongodb_raw_driver',['MongoDB','Mongo','moongoose'],'Uses the MongoDB raw driver with explicit Collection types.')
b('composition-inheritance','composition_over_inheritance',['inheritance','class hierarchies','base class','factory function'],'Prefers composition over inheritance. Factory functions returning plain objects rather than class hierarchies.')
b('error-handling','error_handling_explicit',['error handling','exceptions','try-catch','Result type','error'],'Go-style explicit error returns. Error tuples or Result<T> types rather than thrown exceptions.')
b('db-decision','database_mongodb',['database'],'Decided on MongoDB after evaluating Postgres. Raw driver only.',typ='decision',pinned=True)
b('sqlalchemy-superseded','database_sqlalchemy_old',['SQLAlchemy','Postgres'],'Previously used SQLAlchemy with Postgres. Stack replaced by MongoDB with raw driver.',typ='decision',status='superseded',superseded_by='b-db-decision')
b('kubernetes-entity','kubernetes',['k8s','kube'],'Deploys services to a Kubernetes cluster on EKS. Uses Helm charts checked into the monorepo.',typ='entity')
b('react-entity','react',['ReactJS','react.js'],'Frontend is React 18 with TanStack Query and Tailwind.',typ='entity')
b('auth-open-q','auth_strategy',['JWT','session tokens','auth service','authentication'],'Evaluating JWT vs session tokens for the new auth service. Not decided.',typ='open_question',pinned=True,status='exploratory')
b('narrator-voice','narrator_voice',[],'Close third-person, present tense. No omniscient asides.','writing',pinned=True)
b('narrator-superseded','narrator_voice_old',[],'Previously wrote in first-person narrator voice. Replaced by close third-person.','writing',status='superseded',superseded_by='b-narrator-voice')
b('protagonist-entity','mara',['protagonist'],'Protagonist of the novel. Set in 1970s Lisbon. Does not reconcile with her father; ending is ambiguous.','writing',typ='entity',pinned=True)
b('redis-writing','redis_character',['Redis'],'Redis is a supporting character in the novel — a jazz pianist Mara meets in Lisbon.','writing',typ='entity')
b('redis-code','redis_cache',['Redis'],'Redis is used as a cache and rate-limiter in the backend. Single-node, not clustered.',typ='entity')
b('chapter-structure-q','chapter_structure',['timeline','chapter order','linear','fragmented','structure'],'Debating linear vs fragmented timeline for the novel.','writing',typ='open_question',pinned=True,status='exploratory')
b('other-user-leak-check','other_user_secret',[],'Other user uses Haskell exclusively.',pinned=True,user='other-user')
b('ci-gha-entity','github_actions',['GHA','CI pipeline','github actions'],'CI/CD runs on GitHub Actions. Merge to main triggers a deploy to staging; promotion to prod is manual.',typ='entity')
b('pov-shift-entity','pov_shift',['POV','point of view','perspective shift','viewpoint','chapter 7'],'Chapter 7 uses a deliberate POV shift to an unnamed observer. This is the only chapter that breaks close-third.','writing',typ='entity')
b('hosting-resolved','hosting_decision_old',['Fly.io','self-host','hosting'],'Was evaluating whether to self-host on bare metal or use Fly.io for the auth service.',typ='open_question',resolved_at='2026-08-01T00:00:00Z')
b('prose-style-user-edited','prose_output_style',['prose style','documentation tone'],'Documentation prose should be terse and scannable. No marketing fluff.','writing',pinned=True,user_edited=True)
b('functional-pipeline','functional_pipeline',['pipe','compose','fp'],'Uses functional composition patterns with pipe() to chain data transformations instead of imperative loops.')
b('linting-v0','linting_tslint',['TSLint','tslint'],'Previously used TSLint for linting TypeScript. Replaced by ESLint.',status='superseded',superseded_by='b-linting-v1')
b('linting-v1','linting_eslint',['ESLint','eslint','@typescript-eslint'],'Switched from TSLint to ESLint with @typescript-eslint. Later replaced by Biome.',status='superseded',superseded_by='b-linting-current')
b('linting-current','linting_biome',['Biome','biome','linting','formatter'],'Uses Biome for linting and formatting. Replaced ESLint and Prettier.',pinned=True)
b('vitest-pref','testing_vitest',['Vitest','vitest','jest'],'Uses Vitest for all testing. Replaced Jest.')
b('dead-letter-queue','dead_letter_queue',['dead letter','DLQ','failed messages'],'Failed messages route to a MongoDB-backed dead letter queue with manual replay via admin CLI.',typ='entity')
b('react-expertise','react_expertise_depth',['react depth','react level'],'Deep React expertise — skip basics, engage on architecture and performance trade-offs.','user:universal',status='inferred')
b('auth-depends-redis','auth_service_depends_on_redis',['auth redis','session backend','auth'],'Auth service depends on Redis for session storage. If Redis is down, auth fails open (denies all).',typ='relation',participants=['b-redis-code','b-auth-open-q'])
b('pinned-resolved-interaction','api_versioning_strategy',['API versioning','v2 migration'],'Was debating URL-path versioning vs header-based versioning for the public API.',typ='open_question',pinned=True,resolved_at='2026-09-01T00:00:00Z')
b('zero-reinforcement-fresh','turborepo_monorepo',['Turborepo','turbo','monorepo'],'Monorepo managed by Turborepo. Apps and packages in /apps and /packages directories.',typ='entity')
# Exact upstream query texts, ids, categories and scored assertions; description/notes omitted.
cases=[]
def ids(x): return ['b-'+v for v in x.split()] if isinstance(x,str) else x
M=object()
def c(id,query,rel=M,scope='code',cat='Alias resolution',pinned=None,openq=None,pre=None,budget=None,user=None):
    e={}
    if rel is not M: e['relevantBeliefs']= {'shouldOnlyInclude':ids(rel)} if not isinstance(rel,dict) else rel
    if pinned is not None:e['pinnedFacts']=pinned
    if openq is not None:e['openQuestions']=openq
    if pre is not None:e['personaPrelude']=pre
    d=dict(caseId=id,category=cat,scope=[s if ':' in s else 'domain:'+s for s in scope.split()],query=query,expect=e)
    if budget is not None:d['budget']={'maxBeliefs':budget}
    if user:d['userId']=user
    cases.append(d)
def inc(x):return {'mustInclude':ids(x)}
def exc(x):return {'mustExclude':ids(x)}
ne={'nonEmpty':True}
c('happy-stack-repository-layer','How should I structure my repository layer?','',cat='Type routing and open questions',pinned=inc('db-decision'),openq=inc('auth-open-q'),pre=ne)
c('happy-alias-match-kubernetes',"what's my k8s deploy story?",'kubernetes-entity',pinned=inc(''))
c('alias-kube-resolves','how does kube schedule pods across our CI pipeline nodes?','ci-gha-entity kubernetes-entity')
c('alias-gha-resolves','GHA secrets config','ci-gha-entity')
c('long-query-rambling-kubernetes',"ok so I've been thinking about this for a while and I know we've talked about infrastructure before but I'm circling back because the team had some pushback in the last review and honestly I'm not sure what the right call is anymore. Basically the question is how we should handle rolling deploys for the landing page when it lands — should we do Kubernetes native rolling updates or something more custom, and does it matter that the service holds session state in memory?",'kubernetes-entity',pinned=inc(''),openq=inc('auth-open-q'),pre=ne)
c('out-of-scope-coin-collecting',"what's a good way to grade a 1909-S VDB penny without sending it to PCGS?",'',scope='hobby',cat='Scope disambiguation',pre=ne)
c('happy-writing-scope','How should I open the next chapter with Mara?','',scope='writing',cat='Scope disambiguation',pinned=inc('protagonist-entity'),pre=ne)
c('conflict-scope-disambiguation-redis-writing',"What's Redis doing in chapter three?",'redis-writing',scope='writing',cat='Scope disambiguation')
c('conflict-scope-disambiguation-redis-code','What are we using Redis for?','redis-code',cat='Scope disambiguation')
c('multi-scope-redis-both-surfaces',"What's Redis doing?",'redis-code redis-writing',scope='code writing',cat='Scope disambiguation')
c('conflict-mongodb-supersedes-sqlalchemy',"how's our SQLAlchemy setup these days?",'',cat='Supersession chain exclusion',pinned=inc('db-decision'))
c('edge-empty-query','','',cat='Design boundary cases',pinned={'mustInclude':ids('db-decision'),'mustExclude':ids('auth-open-q comm-response-length')},pre=ne)
c('edge-short-query-raw-passthrough','fastify?','fastify-pref',pinned=inc(''))
c('isolation-user-scoping','what language do I write in?',exc('other-user-leak-check'),cat='Cross-user isolation',pinned=exc('other-user-leak-check'),pre={'mustNotContain':['Haskell']})
c('questions-pinned-active-only','any open questions?','',cat='Type routing and open questions',openq={'mustInclude':ids('auth-open-q'),'mustExclude':ids('chapter-structure-q hosting-resolved')})
c('alias-ts-resolves','TS strict mode question about unknown types','ts-pref',pinned=inc(''))
c('alias-mongo-resolves',"what's the Mongo pattern we're using for collections?",'mongo-raw-driver',pinned=inc(''))
c('alias-reactjs-resolves','how should I structure my ReactJS components here?','react-entity')
c('alias-protagonist-resolves','what does the protagonist want, and does their viewpoint stay consistent in act two?','pov-shift-entity',scope='writing',pinned=inc('protagonist-entity'))
c('canonical-name-match-mara',"tell me about Mara's backstory with her father",'',scope='writing',pinned=inc('protagonist-entity'))
c('alias-pov-shift-resolves','POV rules for chapter 7','pov-shift-entity',scope='writing')
c('alias-base-class-retrieves-composition','should I use a base class hierarchy for these domain objects?','composition-inheritance')
c('alias-exceptions-retrieves-error-handling','when should I throw exceptions versus return them?','error-handling')
c('filter-resolved-at-excluded','should we self-host the landing page or go with Fly.io?','',cat='Type routing and open questions',openq=exc('hosting-resolved'))
c('scope-bleed-questions-code-excludes-writing','any unresolved decisions on this sprint?','',cat='Scope disambiguation',openq={'mustInclude':ids('auth-open-q'),'mustExclude':ids('chapter-structure-q')})
c('persona-prelude-content-present',"what's our stack?",'',cat='Persona prelude content',pre={'nonEmpty':True,'contains':['direct','preamble']})
c('edge-short-query-score-clears','k8s?','kubernetes-entity',pinned=inc(''))
c('edge-whitespace-query','   ','',cat='Design boundary cases',pre=ne)
c('score-boundary-weak-match-filtered','thinking about observability and monitoring dashboards','',cat='Design boundary cases')
c('user-edited-cross-scope-surfaces','summarize what you know about my preferences','',cat='Scope disambiguation',pinned=exc('prose-style-user-edited'))
c('user-edited-absent-without-flag','what are my writing preferences?','',cat='Scope disambiguation',pinned=exc('narrator-voice'))
c('ranking-canonical-name-over-content',"composition over inheritance — what's our pattern?",'composition-inheritance',cat='Ranking stability')
c('messy-typo-blind-spot','how do I configure typscript strict mode?','ts-pref',cat='Fuzzy matching and prefix guards',pinned=inc(''))
c('messy-all-caps-case-insensitive','REACTJS component patterns','react-entity')
c('messy-filler-heavy-extraction',"ugh so like I was wondering you know about how we should think about error handling in our codebase because honestly it's been bugging me for a while and I'm not really sure what the right approach is here",'error-handling')
c('messy-negation-surfaces-topic',"I don't want class hierarchies in this codebase, what should I use instead?",'composition-inheritance')
c('messy-compound-surfaces-two-beliefs','how should ReactJS components handle exceptions from async calls?','react-entity error-handling')
c('cap-stress-compound-six-entities','When our React frontend catches error handling exceptions, how should the Kubernetes pods running MongoDB TypeScript services report to GitHub Actions?','react-entity error-handling ci-gha-entity kubernetes-entity mongo-raw-driver ts-pref')
c('cap-stress-kitchen-sink-exceeds-twelve','Set up our React components to use composition pattern with explicit error handling, wire GitHub Actions CI to deploy Kubernetes pods serving the MongoDB TypeScript API with functional pipeline transforms','react-entity error-handling ci-gha-entity kubernetes-entity mongo-raw-driver ts-pref functional-pipeline')
c('cap-stress-late-position-entity-at-risk','Given our React frontend, the GitHub Actions CI pipeline, the Kubernetes deployment, and the MongoDB TypeScript backend, how should we approach exceptions in the data layer?','ci-gha-entity react-entity kubernetes-entity mongo-raw-driver ts-pref error-handling')
c('fuzzy-single-edit-fires-non-pinned','eror handling patterns','error-handling',cat='Fuzzy matching and prefix guards')
c('fuzzy-scope-filter-discriminates-after-fuzzy-fires','Reedis cache eviction strategy','redis-code',cat='Fuzzy matching and prefix guards')
c('fuzzy-prefix-blocks-transitive-match','mango connection pooling','',cat='Fuzzy matching and prefix guards')
c('fuzzy-abbreviation-prefix-blind-spot','k9s pod scheduling','',cat='Fuzzy matching and prefix guards',pinned=inc(''))
c('fuzzy-two-edit-transposition-blocked','typsecript compilation config','ts-pref',cat='Fuzzy matching and prefix guards',pinned=inc(''))
c('fuzzy-single-term-only-signal','mongod','mongo-raw-driver',cat='Fuzzy matching and prefix guards',pinned=inc(''))
c('universal-scope-belief-content-in-persona-prelude',"what's a good way to grade a 1909-S VDB penny without sending it to PCGS?",'',scope='hobby',cat='Persona prelude content',pre={'nonEmpty':True,'contains':['direct','preamble']})
c('type-isolation-open-question-not-in-relevant','JWT authentication token strategy','',cat='Type routing and open questions',pinned=exc('auth-open-q'),openq=inc('auth-open-q'))
c('multi-hop-supersession-chain-all-excluded','ESLint rules and TSLint configuration','',cat='Supersession chain exclusion',pinned=inc('linting-current'))
c('cross-scope-one-empty-domain-does-not-degrade','What are we using Redis for?','redis-code',scope='code hobby',cat='Scope disambiguation')
c('ranking-orderedbefore-alias-beats-content','explicit error returns in pipe transforms','functional-pipeline error-handling',cat='Ranking stability')
c('cold-start-user-no-beliefs','how should I structure my repository layer?','',cat='Cold start behavior',pre={'isNull':True},user='brand-new-user')
c('narrator-voice-pinned-in-own-scope','','',scope='writing',cat='Persona prelude content',pinned=inc('narrator-voice protagonist-entity prose-style-user-edited'),openq=inc('chapter-structure-q'),pre=ne)
c('multi-scope-prelude-uses-primary-scope-only',"give me an overview of what I'm working on",'',scope='code writing',cat='Persona prelude content',pre=ne)
c('why-it-matters-not-indexed-blind-spot','Vue component architecture','',cat='Design boundary cases')
c('budget-ceiling-evicts-relevant-beliefs','How should I handle errors in my repository layer?','',cat='Budget eviction and capacity',pinned=inc(''),budget=2)
c('negative-guidance-mongoose','suggest a good Mongoose schema for my user model','mongo-raw-driver',cat='Counter-signal retrieval',pinned=inc(''))
c('recency-tiebreak-reinforcement','',cat='Ranking stability',pinned=inc(''))
c('preference-flood-relevant-entity-survives','how does our CI pipeline handle secrets?','ci-gha-entity',cat='Budget eviction and capacity',budget=8)
c('cross-scope-non-user-edited-blocked','tell me about Mara',{'mustExclude':ids('protagonist-entity'),'shouldOnlyInclude':[]},cat='Scope disambiguation',pinned=exc('protagonist-entity'))
c('alias-two-word-shingle-dead-letter','what happens to dead letter messages that fail replay?','dead-letter-queue')
c('alias-dlq-abbreviation-resolves','how do we replay items from the DLQ?','dead-letter-queue')
c('counter-signal-jest-surfaces-vitest','should I configure Jest for this project?','vitest-pref',cat='Counter-signal retrieval')
c('relation-type-expands-participants','what are the auth service dependencies and failure modes?','auth-depends-redis redis-code',cat='Relation expansion',openq=inc('auth-open-q'))
c('relation-participant-open-question-routes-correctly','session backend dependency graph','auth-depends-redis redis-code',cat='Relation expansion',openq=inc('auth-open-q'),pinned=exc('auth-open-q'))
c('relation-expansion-respects-scope-filter','auth redis failure modes',{'shouldOnlyInclude':ids('auth-depends-redis redis-code'),'mustExclude':ids('redis-writing')},cat='Relation expansion')
c('subtype-expertise-retrieval',"what's my react depth — should you explain hooks to me?",'react-entity',cat='Type routing and open questions')
c('pinned-plus-resolved-excluded-from-questions','API versioning strategy for v2 migration','',cat='Type routing and open questions',pinned=exc('pinned-resolved-interaction'),openq=exc('pinned-resolved-interaction'))
c('zero-reinforcement-fresh-belief-surfaces','how is our Turborepo monorepo structured?','zero-reinforcement-fresh',cat='Budget eviction and capacity')
c('budget-zero-graceful-empty',"what's our stack?",'',cat='Budget eviction and capacity',pinned=inc(''),budget=0)
c('budget-one-pinned-wins-over-relevant','MongoDB collection typing patterns','',cat='Budget eviction and capacity',pinned=inc('db-decision'),budget=1)
c('multi-scope-code-writing-dedup-no-double-surface','what tools do we use?','',scope='code writing',cat='Scope disambiguation')
c('empty-alias-belief-content-path-only','should you give me one recommendation or a list of options?','',cat='Design boundary cases')
c('universal-scope-explicit-query-no-relevant','how do I want you to respond to me?','',scope='user:universal',cat='Scope disambiguation',pre={'nonEmpty':True,'contains':['direct']})
c('negation-query-still-surfaces-rejected-tool','I do NOT want to use Express, what should I use instead?','fastify-pref',cat='Design boundary cases')
c('superseded-chain-query-terminal-only','TSLint migration path','',cat='Supersession chain exclusion',pinned=inc('linting-current'))
c('relation-does-not-appear-as-entity','Redis session backend dependency graph','auth-depends-redis redis-code',cat='Relation expansion',openq=inc('auth-open-q'))
assert len(seeds)==35, len(seeds)
assert len(cases)==77, len(cases)
meta={
 'dataset':'precisionmembench_projection', 'source_kind':'upstream_authored_synthetic_fixture',
 'upstream':'https://github.com/tenurehq/precisionMemBench',
 'revision':'61a712a1754e7d27b6dee42da32a2c214b003ece',
 'seed_blob_sha':'2d9c9daf55c075f0f64a9a9f71db61feda7ad4c2',
 'cases_blob_sha':'dd0ee63d91b9b19f4c891463ab104bb2ec0485bd',
 'license':'MIT', 'author':'Jeffrey Flynt', 'retrieved_date':'2026-10-15',
 'acquisition':'Manually transcribed field projection from GitHub connector; full raw-byte download unavailable in build environment.',
 'transformation':'Seed retains id, user, type, name, aliases, content, scope, status, pinned, supersession, resolution, edit flag, participants; missing optional fields normalized; cases omit description/notes; queries and assertions unchanged.',
 'limitations':['Not the full upstream benchmark protocol; single-turn assertions only; 12 session assertions are catalogued, not bundled.', 'Descriptions contain framework-specific assumptions; report is diagnostic, not a universal PA score.', 'One case contains no effective assertion and is explicitly unscored.', 'Original prose inconsistency "fails open (denies all)" is preserved; no correction made.']
}
(ROOT/'data/pmb_projection.json').write_text(json.dumps(dict(provenance=meta,beliefs=seeds,cases=cases),ensure_ascii=False,indent=2)+'\n')
print('projected',len(seeds),'beliefs',len(cases),'cases')
