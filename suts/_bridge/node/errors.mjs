// Only structured classification is added to RPC data, never response headers/body.
export function errorData(error) {
 const data={type:error?.constructor?.name??'Error'}, seen=new Set();
 function inspect(value) {
  if(value==null||seen.has(value))return;
  seen.add(value);
  if(typeof value==='string') {
   try {const r=inspect(JSON.parse(value));if(r)return r;}catch{}
   const r=value.includes('memeval_governor')?value.match(/["']?reason["']?\s*:\s*["'](exhausted|budget)["']/):null;
   const h=value.match(/x-memeval-governor["']?\s*:\s*["']?(exhausted|budget)\b/i);
   return r?.[1]??h?.[1]?.toLowerCase();
  }
  if(typeof value!=='object')return;
  if(value.type==='memeval_governor'&&['exhausted','budget'].includes(value.reason))return value.reason;
  const headers=value.headers;
  if(headers) {
   const h=typeof headers.get==='function'?headers.get('X-Memeval-Governor'):Object.entries(headers).find(([k])=>k.toLowerCase()==='x-memeval-governor')?.[1];
   if(['exhausted','budget'].includes(h))return h;
  }
  for(const key of ['response','body','error','cause','message']){const r=inspect(value[key]);if(r)return r;}
 }
 const reason=inspect(error);if(reason)data.governor=reason;
 return data;
}
