import { Component, Input, ChangeDetectorRef, inject } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { api, Catalog, Dashboard, Service, Extra, Specialist, salonDate, timeLabel, toInstant, statuses } from './api';

type Edit = { id:number; name:string; description:string; bio:string; active:boolean; price:number; minutes:number; p60:number|null; p90:number|null; p120:number|null };
@Component({selector:'app-admin',standalone:true,imports:[FormsModule],templateUrl:'./admin.html'})
export class AdminComponent {
 @Input() catalog:Catalog={services:[],extras:[],specialists:[],timezone:'Asia/Bishkek',currency:'KGS',telegram_bot:''};
 private cd=inject(ChangeDetectorRef); logged=false; checking=true; busy=false; error=''; notice=''; password=''; date=salonDate('Asia/Bishkek'); tab='schedule'; data:Dashboard|null=null; statuses=statuses;
 shiftMaster=0; shiftStart='09:00'; shiftEnd='18:00'; editor:Edit|null=null; editKind='services';
 ngOnInit(){void this.probe()}
 async probe(){try{this.data=await api<Dashboard>('/admin/dashboard?date='+this.date);this.logged=true}catch{}finally{this.checking=false;this.cd.markForCheck()}}
 time(v:string){return timeLabel(v,this.catalog.timezone)}
 money(n:number){return new Intl.NumberFormat('ru').format(n)+' '+(this.catalog.currency==='KGS'?'сом':this.catalog.currency)}
 master(id:number){return this.data?.catalog.specialists.find(s=>s.id===id)?.name||'Мастер'}
 async run(fn:()=>Promise<void>){if(this.busy)return;this.busy=true;this.error='';this.notice='';try{await fn()}catch(e){this.error=e instanceof Error?e.message:'Не удалось связаться с сервером';if(this.error.includes('сессия')||this.error.includes('Сессия')||this.error.includes('Войдите'))this.logged=false}finally{this.busy=false;this.cd.markForCheck()}}
 async reload(){this.data=await api<Dashboard>('/admin/dashboard?date='+this.date);if(!this.shiftMaster)this.shiftMaster=this.data.catalog.specialists[0]?.id||0}
 refresh(){void this.run(()=>this.reload())}
 login(){void this.run(async()=>{await api('/admin/login','POST',{password:this.password});this.password='';this.logged=true;await this.reload()})}
 logout(){void this.run(async()=>{await api('/admin/logout','POST',{});this.logged=false;this.data=null})}
 addShift(){void this.run(async()=>{await api('/admin/shifts','POST',{id:0,specialist_id:this.shiftMaster,starts_at:toInstant(this.date+'T'+this.shiftStart,this.catalog.timezone),ends_at:toInstant(this.date+'T'+this.shiftEnd,this.catalog.timezone)});await this.reload();this.notice='Рабочий интервал добавлен'})}
 removeShift(id:number){void this.run(async()=>{await api('/admin/shifts/'+id,'DELETE');await this.reload();this.notice='Рабочий интервал удалён'})}
 status(id:number,status:string){if(status==='cancelled'&&!window.confirm('Отменить запись и уведомить клиента?'))return;void this.run(async()=>{await api('/admin/bookings/'+id+'/status','POST',{status});await this.reload();this.notice='Статус записи обновлён'})}
 edit(kind:string,item?:Service|Extra|Specialist){this.editKind=kind;const s=item as Service|undefined;const x=item as Extra|undefined;this.editor={id:item?.id||0,name:item?.name||'',description:s?.description||'',bio:(item as Specialist|undefined)?.bio||'',active:item?.active??true,price:x?.price||0,minutes:x?.minutes||0,p60:s?.prices?.['60']??null,p90:s?.prices?.['90']??null,p120:s?.prices?.['120']??null}}
 save(){void this.run(async()=>{const x=this.editor;if(!x)return;const prices:Record<string,number>={};for(const [d,p] of [['60',x.p60],['90',x.p90],['120',x.p120]] as const){if(p!==null)prices[d]=p};await api('/admin/'+this.editKind,'POST',{id:x.id,name:x.name,active:x.active,...(this.editKind==='services'?{description:x.description,prices}:this.editKind==='extras'?{price:x.price,minutes:x.minutes}:{bio:x.bio})});await this.reload();this.editor=null;this.notice='Изменения сохранены. Каталог сайта и бота обновлён.'})}
 prices(s:Service){return Object.entries(s.prices).map(([d,p])=>d+' мин · '+this.money(p)).join(' / ')}
}
