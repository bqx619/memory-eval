import { pathToFileURL } from 'node:url';
import { serve } from './protocol.mjs';
export * from './protocol.mjs';
if(process.argv[1] && import.meta.url===pathToFileURL(process.argv[1]).href) {
  async function main() {
  const args=process.argv.slice(2), get=k=>args.includes(k)?args[args.indexOf(k)+1]:undefined;
  process.stdout.write=(...args)=>process.stderr.write(...args);
  const mod=await import(pathToFileURL(get('--adapter')).href);
  await serve(new mod.Adapter(JSON.parse(get('--config')||'{}')),Number(get('--max-frame-bytes')||16*1024*1024));
  }
  main().catch(e=>{console.error(e);process.exitCode=1;});
}
