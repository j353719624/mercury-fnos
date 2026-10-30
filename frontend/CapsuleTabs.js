import{$i as e,Ai as a,Ia as t,Ki as l,Mi as o,Ni as r,Ti as s,Wi as i,Xi as n,aa as u,ba as c,ca as f,da as v,ji as d,na as p,ta as m,za as h,zi as g}from"./index-quJxVxr2.js";import{t as y}from"./_plugin-vue_export-helper-B4KiXbT9.js";
var K="bg-gray-200/30 dark:bg-gray-800/30 backdrop-blur-md rounded-full p-1 inline-flex items-center gap-1 border border-white/80 dark:border-white/10 shadow-sm overflow-x-auto no-scrollbar max-w-full";
var B="px-4 py-1.5 rounded-full cursor-pointer transition-all duration-300 inline-flex items-center gap-2 text-sm font-medium select-none shrink-0";
var ON="bg-[var(--el-color-primary)] text-[var(--el-bg-color)] shadow-md transform scale-100";
var OFF="text-[var(--el-text-color-regular)] hover:text-[var(--el-text-color-primary)] hover:bg-[var(--el-fill-color-dark)]";
var b=y(g({__name:"index",props:i({options:{}},{modelValue:{required:!0},modelModifiers:{}}),emits:i(["change"],["update:modelValue"]),setup(i,{emit:g,slots:n}){
  var b=f(i,"modelValue");
  var pick=function(v){b.value!==v&&(b.value=v,g("change",v))};
  var icon=function(item,act){
    if("object"==typeof item.icon||"function"==typeof item.icon){
      return d(u(item.icon),{key:0,class:t(["w-4 h-4",act?"opacity-100":"opacity-70"])},null,8,["class"]);
    }
    if("string"==typeof item.icon){
      return a("i",{key:1,class:t([item.icon,act?"opacity-100":"opacity-70"])},null,2);
    }
    return o("",!0);
  };
  var pill=function(item){
    var act=b.value===item.value;
    return a("div",{key:item.value,class:t([B,String(b.value).startsWith(String(item.value))?ON:OFF]),onClick:function(){pick(item.value)}},[
      icon(item,act),
      a("span",null,h(item.label||item.value),1)
    ]);
  };
  return function(){
    var list=i.options||[];
    var rows=list.length>12?[list.slice(0,8),list.slice(8)]:[list];
    return a("div",{class:"flex flex-wrap items-start gap-1.5 max-w-full"},rows.map(function(row,idx){
      return a("div",{key:"row"+idx,class:idx?K+" mt-1.5":K},row.map(pill));
    }));
  };
}}),[["__scopeId","data-v-3f07a8c3"]]);
export{b as t};
