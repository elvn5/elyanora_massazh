export interface Specialist { id:number; name:string; bio:string; active:boolean }
export interface Service { id:number; name:string; description:string; prices:Record<string,number>; active:boolean }
export interface Extra { id:number; name:string; price:number; minutes:number; active:boolean }
export interface Catalog { specialists:Specialist[]; services:Service[]; extras:Extra[]; timezone:string; currency:string; telegram_bot:string; reminder_24h?:boolean; reminder_2h?:boolean }
export interface Booking { id:number; token:string; specialist_id:number; specialist:string; service_id:number; service:string; duration:number; extras:Extra[]; total:number; starts_at:string; ends_at:string; name:string; phone:string; status:string; version:number; telegram_chat?:number }
export interface Shift { id:number; specialist_id:number; starts_at:string; ends_at:string }
export interface Dashboard { weekly:{date:string;bookings:number;revenue:number}[]; bookings:Booking[]; shifts:Shift[]; catalog:Catalog; stats:Record<string,number> }
export interface Page<T> { items:T[]; total:number; revenue?:number }
export interface Client { phone:string; name:string; bookings:number; completed:number; cancelled:number; no_show:number; spent:number; first_at:string; last_visit:string|null; next_visit:string|null; telegram:boolean }
export async function api<T>(path:string,method='GET',body?:unknown):Promise<T> {
 const response=await fetch('/api'+path,{method,credentials:'same-origin',headers:body===undefined?{}:{'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});
 const data=await response.json().catch(()=>({error:'Сервер временно недоступен'}));
 if(!response.ok) throw new Error(data.error||'Не удалось выполнить запрос');
 return data as T;
}
export function salonDate(tz:string,delta=0):string {const parts=new Intl.DateTimeFormat('en-CA',{timeZone:tz,year:'numeric',month:'2-digit',day:'2-digit'}).formatToParts(new Date());const get=(s:string)=>parts.find(p=>p.type===s)!.value;const d=new Date(`${get('year')}-${get('month')}-${get('day')}T12:00:00Z`);d.setUTCDate(d.getUTCDate()+delta);return d.toISOString().slice(0,10)}
export function toInstant(local:string,tz:string):string { const fmt=new Intl.DateTimeFormat('sv-SE',{timeZone:tz,year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',hourCycle:'h23'});const target=Date.parse(local+'Z');let value=target;for(let i=0;i<3;i++){const formatted=fmt.format(new Date(value)).replace(' ','T');value+=target-Date.parse(formatted+'Z')};if(fmt.format(new Date(value)).replace(' ','T').slice(0,16)!==local.slice(0,16))throw new Error('Такого местного времени не существует');return new Date(value).toISOString() }
export function timeLabel(value:string,tz:string):string {return new Intl.DateTimeFormat('ru',{timeZone:tz,hour:'2-digit',minute:'2-digit'}).format(new Date(value))}
export function dateLabel(value:string,tz:string):string {return new Intl.DateTimeFormat('ru',{timeZone:tz,day:'numeric',month:'long',weekday:'short'}).format(new Date(value))}
export const statuses:Record<string,string>={confirmed:'Подтверждена',cancelled:'Отменена',completed:'Завершена',no_show:'Неявка'};
