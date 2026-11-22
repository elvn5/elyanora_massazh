import { Component, ChangeDetectorRef, inject } from '@angular/core';
import { bootstrapApplication } from '@angular/platform-browser';
import { FormsModule } from '@angular/forms';
import { AdminComponent } from './admin';
import { api, Booking, Catalog, Service, timeLabel, dateLabel, salonDate, statuses } from './api';

@Component({selector:'app-root',standalone:true,imports:[FormsModule,AdminComponent],templateUrl:'./app.html'})
class App {
 private cd=inject(ChangeDetectorRef);
 catalog:Catalog={services:[],specialists:[],extras:[],timezone:'Asia/Bishkek',currency:'KGS',telegram_bot:''};
 loading=true; busy=false; error=''; notice=''; admin=location.pathname.startsWith('/admin'); step=1;
 serviceID=0; specialistID=0; duration=60; extras:number[]=[]; date=''; selectedSlot=''; slots:string[]=[]; slotsLoading=false; slotsLoaded=false; slotRequest=0;
 name=''; phone=''; consent=false; booking:Booking|null=null; moving=false; password='';
 minDate='';maxDate=''; statuses=statuses;
 constructor(){void this.init()}
 async init(){this.loading=true;this.error='';try{this.catalog=await api<Catalog>('/catalog');this.minDate=salonDate(this.catalog.timezone);this.maxDate=salonDate(this.catalog.timezone,90);this.date=this.minDate;this.serviceID=this.catalog.services[0]?.id||0;this.specialistID=this.catalog.specialists[0]?.id||0;this.duration=this.durations[0]||60;const t=new URLSearchParams(location.search).get('booking');if(t)this.booking=await api<Booking>('/manage/'+encodeURIComponent(t));}catch(e){this.error=this.message(e)}finally{this.loading=false;this.cd.markForCheck()}}
 message(e:unknown){return e instanceof Error?e.message:'Нет связи с сервером. Попробуйте снова.'}
 get service(){return this.catalog.services.find(x=>x.id===this.serviceID)}
 get specialist(){return this.catalog.specialists.find(x=>x.id===this.specialistID)}
 get durations(){return Object.keys(this.service?.prices||{}).map(Number).sort((a,b)=>a-b)}
 get chosenExtras(){return this.catalog.extras.filter(x=>this.extras.includes(x.id))}
 get total(){return (this.service?.prices[this.duration]||0)+this.chosenExtras.reduce((n,x)=>n+x.price,0)}
 get minutes(){return this.duration+this.chosenExtras.reduce((n,x)=>n+x.minutes,0)}
 get currency(){return this.catalog.currency==='KGS'?'сом':this.catalog.currency}
 get canChange(){return this.booking?.status==='confirmed'&&new Date(this.booking.starts_at)>new Date()}
 money(n:number){return new Intl.NumberFormat('ru').format(n)+' '+this.currency}
 time(v:string){return timeLabel(v,this.catalog.timezone)}
 day(v:string){return dateLabel(v,this.catalog.timezone)}
 chooseService(s:Service){this.serviceID=s.id;if(!this.durations.includes(this.duration))this.duration=this.durations[0];this.resetSlots()}
 toggleExtra(id:number){this.extras=this.extras.includes(id)?this.extras.filter(x=>x!==id):[...this.extras,id];this.resetSlots()}
 resetSlots(){this.slotRequest++;this.selectedSlot='';this.slots=[];this.slotsLoaded=false;this.slotsLoading=false}
 async loadSlots(){const request=++this.slotRequest;this.selectedSlot='';this.error='';this.slotsLoading=true;this.slotsLoaded=false;try{const q=new URLSearchParams({specialist_id:String(this.specialistID),service_id:String(this.serviceID),duration:String(this.duration),date:this.date,extras:this.extras.join(',')});if(this.moving&&this.booking)q.set('token',this.booking.token);const result=await api<string[]>('/slots?'+q);if(request===this.slotRequest){this.slots=result;this.slotsLoaded=true}}catch(e){if(request===this.slotRequest)this.error=this.message(e)}finally{if(request===this.slotRequest)this.slotsLoading=false;this.cd.markForCheck()}}
 async next(){this.error='';if(this.step===1){if(!this.serviceID||!this.durations.includes(this.duration)){this.error='Выберите услугу и длительность';return};this.step=2;await this.loadSlots()}else if(this.step===2){if(!this.selectedSlot){this.error='Выберите свободное время';return};this.step=3};window.scrollTo({top:0,behavior:'smooth'})}
 async submit(){if(this.busy)return;this.busy=true;this.error='';try{this.booking=await api<Booking>('/bookings','POST',{specialist_id:this.specialistID,service_id:this.serviceID,duration:this.duration,extra_ids:this.extras,starts_at:this.selectedSlot,name:this.name,phone:this.phone,consent:this.consent});history.replaceState(null,'','/?booking='+this.booking.token);this.notice='Вы записаны. Время для себя забронировано.';window.scrollTo({top:0,behavior:'smooth'})}catch(e){this.error=this.message(e);if(this.error.includes('время')||this.error.includes('Время')){this.step=2;this.selectedSlot='';this.slots=[];this.slotsLoaded=false}}finally{this.busy=false;this.cd.markForCheck()}}
 async cancel(){if(!this.booking||this.busy)return;this.busy=true;this.error='';try{this.booking=await api<Booking>('/manage/'+this.booking.token+'/cancel','POST',{});this.moving=false;this.notice='Запись отменена. Будем рады видеть вас снова.'}catch(e){this.error=this.message(e)}finally{this.busy=false;this.cd.markForCheck()}}
 async startMove(){this.moving=true;this.date=this.minDate;await this.loadSlots()}
 async move(){if(!this.booking||!this.selectedSlot||this.busy)return;this.busy=true;this.error='';try{this.booking=await api<Booking>('/manage/'+this.booking.token+'/reschedule','POST',{starts_at:this.selectedSlot});this.moving=false;this.notice='Запись перенесена. Напоминания обновлены.'}catch(e){this.error=this.message(e);this.selectedSlot='';this.slotsLoaded=false;this.slots=[]}finally{this.busy=false;this.cd.markForCheck()}}
 get manageLink(){return location.origin+'/?booking='+this.booking?.token}
 get telegramLink(){return 'https://t.me/'+this.catalog.telegram_bot+'?start='+this.booking?.token}
 async copy(){try{await navigator.clipboard.writeText(this.manageLink);this.notice='Ссылка скопирована'}catch{this.notice='Скопируйте ссылку из адресной строки'}this.cd.markForCheck()}
}
bootstrapApplication(App).catch(console.error);
