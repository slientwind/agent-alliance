"""Build the self-contained review canvas, SVG and single-sheet diagrams.net file."""
from collections import defaultdict
from pathlib import Path
from html import escape
import json
import math
import xml.etree.ElementTree as ET

from model import WIDTH, HEIGHT, modules, interfaces, inside, research, paired_ports, prior_components

HERE = Path(__file__).resolve().parent
LEGACY = HERE.parent / 'agent-kernel'
OUT = HERE
PAD, TOP, CW_GAP, CH, RH_GAP, BOTTOM = 18, 76, 22, 122, 24, 20
INK, SUB = '#162f42', '#435d6c'
TINTS = {'live':('#e8f5ee','#42836a','已有'), 'extend':('#e8f0fb','#527ba6','扩展'), 'new':('#fff1dd','#a16c24','拟建')}
PALETTES = {'gateway':'#225977','security':'#674d74','builder':'#386849','control':'#285776','memory':'#795e36','loop':'#2e536f','supply':'#34684f','harness':'#284f70','knowledge':'#755735','work':'#386849','runtime':'#254c6a','backends':'#725836','delivery':'#2b6761','infra':'#45536a'}
EDGE={'call':'#3c5e73','read':'#317568','event':'#85557a'}
card_map, group_map, segments, notes, sources = {}, {}, [], defaultdict(list), {}
for g in modules:
    rows=math.ceil(len(g.cards)/g.columns)
    gh=TOP+rows*CH+(rows-1)*RH_GAP+BOTTOM
    group_map[g.key]={'id':g.key,'title':g.title,'owner':g.owner,'box':(g.x,g.y,g.width,gh),'cards':[],'sources':g.sources}
    for i,(key,status,title,lines) in enumerate(g.cards):
        assert key not in card_map
        cw=(g.width-2*PAD-(g.columns-1)*CW_GAP)/g.columns
        col,row=i%g.columns,i//g.columns
        card_map[key]=dict(id=key,title=title,status=status,lines=lines,group=g.key,
                           box=(g.x+PAD+col*(cw+CW_GAP),g.y+TOP+row*(CH+RH_GAP),cw,CH))
        group_map[g.key]['cards'].append(key)
def tokens(svg):return [t.text for t in svg.findall('.//{*}text') if t.text]
for p in sorted([*LEGACY.glob('*.svg'),*HERE.parent.glob('*.svg')]):
    if p.parent==HERE:continue
    name=p.stem if p.parent==LEGACY else '../'+p.stem
    root=ET.parse(p).getroot()
    groups=[n for n in root.findall('.//{*}g') if n.get('data-name')]
    if name in prior_components:
        assignments=prior_components[name].split()
        assert len(groups)==len(assignments),(name,len(groups),len(assignments))
        for node,key in zip(groups,assignments):
            assert key in card_map
            notes[key].append({'sheet':name,'lines':tokens(node)})
    sources[name]={'title':(root.findtext('{*}title') or name).split(' · ',1)[-1],
                   'lines':tokens(root), 'links':[{'url':a.attrib['href'],'title':next(iter(tokens(a)), '来源文件')} for a in root.findall('.//{*}a') if a.get('href','').startswith('https://')], 'url':('../agent-kernel/'+p.name if p.parent==LEGACY else '../'+p.name)}
for m in group_map.values():
    for k in m['sources']: assert k in sources
for _,_,k in research: assert k in sources
def n(v):return str(round(v,1)).removesuffix('.0')
def txt(x,y,value,size=18,weight=450,color=INK,extra=''):
    return f'<text x="{n(x)}" y="{n(y)}" font-size="{size}" font-weight="{weight}" fill="{color}" {extra}>{escape(value)}</text>'
def rect(x,y,w,h,fill,stroke='none',r=5,extra=''):
    return f'<rect x="{n(x)}" y="{n(y)}" width="{n(w)}" height="{n(h)}" rx="{r}" fill="{fill}" stroke="{stroke}" {extra}/>'
def text_width(value,size=18):
    return sum(.58 if ord(ch)<128 else 1 for ch in value)*size
def port(box,spec):
    x,y,w,h=box;side,val=spec.split(':');t=float(val)
    return {'L':(x,y+h*t),'R':(x+w,y+h*t),'T':(x+w*t,y),'B':(x+w*t,y+h)}[side]
def compact(points):
    res=[]
    for x,y in points:
        p=(round(x,1),round(y,1))
        if res and p==res[-1]:continue
        if len(res)>1 and (res[-2][0]==res[-1][0]==p[0] or res[-2][1]==res[-1][1]==p[1]):res.pop()
        res.append(p)
    return res
def orth(points,first_axis):
    res=[points[0]]
    for i,(x,y) in enumerate(points[1:]):
        a,b=res[-1]
        if a!=x and b!=y:res.append((x,b) if (i==0 and first_axis=='x') or (i>0 and abs(a-x)>abs(b-y)) else (a,y))
        res.append((x,y))
    return compact(res)
# Route local links in the reserved card gutters; only the first/last stub enters a card boundary.
def local_route(a,b):
    x,y,w,h=a['box'];X,Y,W,H=b['box']
    if abs(y-Y)<1 and abs(x-X)<w+CW_GAP+1:
        return [(x+w,y+h/2),(X,Y+H/2)] if x<X else [(x,y+h/2),(X+W,Y+H/2)]
    if abs(x-X)<1 and abs(y-Y)<h+RH_GAP+1:
        return [(x+w/2,y+h),(X+W/2,Y)] if y<Y else [(x+w/2,y),(X+W/2,Y+H)]
    if y!=Y:
        trunk=y+h+RH_GAP/2 if y<Y else y-RH_GAP/2
        if abs(y-Y)<h+RH_GAP+1:return compact([(x+w*.57,y+h if y<Y else y),(x+w*.57,trunk),(X+W*.43,trunk),(X+W*.43,Y if y<Y else Y+H)])
    # Non-adjacent cards on a row use the clear lane above that row.
    return compact([(x+w*.48,y),(x+w*.48,y-8),(X+W*.55,y-8),(X+W*.55,Y)])
for a,b,kind,label in inside:
    A,B=card_map[a],card_map[b];assert A['group']==B['group']
    segments.append(dict(a=a,b=b,kind=kind,label=label,pts=local_route(A,B),group=A['group']))
for ga,gb,a,b,kind,label,pa,pb,bends,labelxy in interfaces:
    assert (card_map[a]['group'],card_map[b]['group'])==(ga,gb),(a,b)
    A,B=group_map[ga],group_map[gb]
    start,end=port(A['box'],pa),port(B['box'],pb)
    if not bends and pa[0] in 'LR' and pb[0] in 'LR':
        mid=(start[0]+end[0])/2;bends=[(mid,start[1]),(mid,end[1])]
    elif not bends and pa[0] in 'TB' and pb[0] in 'TB':
        mid=(start[1]+end[1])/2;bends=[(start[0],mid),(end[0],mid)]
    segments.append(dict(a=a,b=b,kind=kind,label=label,pts=orth([start,*bends,end],'x' if pa[0] in 'LR' else 'y'),outer=(ga,gb),labelxy=labelxy))
badges=defaultdict(list)
for code,a,b,kind,label in paired_ports:
    assert a in card_map and b in card_map
    badges[a].append(code+'↗');badges[b].append(code+'↙')
    segments.append(dict(a=a,b=b,kind=kind,label=label,code=code))

def port_notes(key):
    return [f'{e["code"]} · {card_map[e["a"]]["title"]} → {card_map[e["b"]]["title"]} · {e["label"]}' for e in segments if 'code' in e and key in (e['a'], e['b'])]

NS='http://www.w3.org/2000/svg'
font='system-ui, -apple-system, PingFang SC, Microsoft YaHei, sans-serif'
def svg_edge(e,i):
    if 'pts' not in e:return ''
    color=EDGE[e['kind']];pts=e['pts'];outer='outer' in e
    d='M'+' L'.join(' '.join(map(n,p)) for p in pts)
    dash='stroke-dasharray="8 5"' if e['kind']=='event' else ''
    return f'<path class="wire" data-index="{i}" d="{d}" fill="none" stroke="{color}" stroke-width="{2.8 if outer else 1.7}" stroke-linejoin="round" {dash} marker-end="url(#arrow-{e["kind"]})"><title>{escape(e["label"])}</title></path>'
def text_sized(x,y,s,width,sz,lower,weight,color):
    size=min(sz, max(lower, (width-3)/max(1,text_width(s,1))))
    return txt(x,y,s,round(size,1),weight,color)
def render_svg():
    svg=[f'<svg xmlns="{NS}" width="{WIDTH}" height="{HEIGHT}" viewBox="0 0 {WIDTH} {HEIGHT}" role="img" aria-labelledby="map-title map-desc" font-family="{font}">',
         '<title id="map-title">Agent Alliance · 企业 Agent 平台完整工程架构</title><desc id="map-desc">一张画布展示企业入口、安全、构建、记忆、知识、Loop、Harness、Runtime、交付及部署。带字母的同号端口连接远端模块。</desc><defs>']
    for k,col in EDGE.items():
        svg.append(f'<marker id="arrow-{k}" markerWidth="8" markerHeight="8" refX="7.2" refY="4" orient="auto" markerUnits="userSpaceOnUse"><path d="M0 0L8 4L0 8" fill="{col}"/></marker>')
    svg+=['</defs>',rect(0,0,WIDTH,HEIGHT,'#f7f9fa',r=0),rect(0,0,WIDTH,141,'#163141',r=0),
          txt(48,66,'Agent Alliance  /  通用企业 Agent 平台工程全景',47,690,'#ffffff'),
          txt(50,109,'单企业独立实例    /    桌面网页对话与企业 IM 接入    /    固定计划与有界自主执行',22,420,'#d2e0e6')]
    for i,(status,(bg,line,label)) in enumerate(TINTS.items()):
        x=1920+i*130;svg.extend([rect(x,43,110,37,bg,line,6),txt(x+32,69,label,20,620,line)])
    svg.extend([txt(1920,108,'实线：调用或读取　　紫虚线：异步回执　　同号端口：跨区依赖',20,440,'#dce8ed')])
    for m in modules:
        group=group_map[m.key];x,y,w,h=group['box'];color=PALETTES[m.key]
        svg.extend([f'<g class="module" data-module="{m.key}" id="g-{m.key}">',
                    rect(x,y,w,h,'#ffffff','#a3b5bf',12,'stroke-width="2"'),
                    rect(x+1,y+1,9,h-2,color,r=4),
                    txt(x+24,y+38,m.title,29,690,color),
                    txt(x+25,y+62,m.owner,17,470,SUB),'</g>'])
    svg+= [svg_edge(e,i) for i,e in enumerate(segments)]
    for k,card in card_map.items():
        x,y,w,h=card['box'];bg,outline,label=TINTS[card['status']]
        svg.extend([f'<g class="card" id="n-{k}" data-key="{k}" tabindex="0" role="button" aria-label="{escape(card["title"])}">',
                    f'<title>{escape(chr(10).join([card["title"]+" · "+label,*port_notes(k)]))}</title><desc>'+escape('\n'.join([*card['lines'],*[l for p in notes[k] for l in p['lines']]]))+'</desc>',
                    rect(x,y,w,h,'#fbfcfd',outline,7,'stroke-width="1.6"'+(' stroke-dasharray="6 3"' if card['status']=='new' else '')),
                    text_sized(x+12,y+31,card['title'],w-69,23,16,670,INK),
                    rect(x+w-49,y+10,39,24,bg,r=4),txt(x+w-45,y+27,label,15,610,outline)])
        for j,line in enumerate(card['lines']):svg.append(text_sized(x+12,y+58+23*j,line,w-23,18,13,440,SUB))
        if badges[k]:
            tag=' · '.join(badges[k]);tw=text_width(tag,13)+10
            svg.extend([rect(x+w-tw-6,y+h-18,tw,17,'#e7edf1',r=3),
                        txt(x+w-tw-1,y+h-5,tag,13,670,'#385468')])
        svg.append('</g>')
    for e in segments:
        if 'labelxy' not in e:continue
        x,y=e['labelxy'];s=e['label'];sz=16 if len(s)>7 else 17;w=text_width(s,sz)+16
        rotation=f' transform="rotate(-90 {x} {y})"' if e['a']=='notify' and e['b']=='projection' else ''
        svg+=[f'<g{rotation}>',rect(x-w/2,y-15,w,23,'#f7f9fa',r=4),txt(x,y+2,s,sz,620,EDGE[e['kind']],'text-anchor="middle"'),'</g>']
    y=3485
    svg.extend([txt(49,y,'架构取舍的调研来源',26,660,INK),txt(358,y,'个人产品机制映射到企业版的模块边界；商业产品以公开接口及可验证行为为依据。',20,450,SUB)])
    for j,(name,note,ref) in enumerate(research):
        x=49+j*445
        svg+= [f'<g class="research" data-ref="{ref}" tabindex="0" role="button">',txt(x,y+48,name,22,660,INK),txt(x,y+79,note,17,440,SUB),'</g>']
    svg.append(txt(49,3617,'商业产品行为对照：ChatGPT Work、Claude Cowork、Manus、Kimi Agent、Meta Muse　　　　　　桌面 Web 设计对照：ChatGPT、Claude、Gemini',18,480,SUB))
    svg.append('</svg>');return ''.join(svg)

def render_drawio():
    root=ET.Element('mxfile',host='app.diagrams.net',type='device',pages='1')
    page=ET.SubElement(root,'diagram',id='enterprise-platform',name='企业 Agent 平台 · 全景')
    graph=ET.SubElement(page,'mxGraphModel',grid='1',gridSize='10',page='1',pageWidth=str(WIDTH),pageHeight=str(HEIGHT),background='#f7f9fa')
    tree=ET.SubElement(graph,'root')
    ET.SubElement(tree,'mxCell',id='0'); ET.SubElement(tree,'mxCell',id='1',parent='0')
    def vertex(uid,label,style,box,parent='1',tip=''):
        owner=tree
        if tip:owner=ET.SubElement(tree,'object',id=uid,label=label,tooltip=tip)
        attrs={'parent':parent,'vertex':'1','style':style}
        if not tip:attrs.update(id=uid,value=label)
        o=ET.SubElement(owner,'mxCell',attrs)
        x,y,w,h=box;ET.SubElement(o,'mxGeometry',x=n(x),y=n(y),width=n(w),height=n(h),attrib={'as':'geometry'})
    base='html=1;whiteSpace=wrap;rounded=1;arcSize=5;align=left;verticalAlign=top;fontFamily=Arial;'
    vertex('heading','<b style="font-size:40px">Agent Alliance / 通用企业 Agent 平台工程全景</b><br>单企业独立实例　·　网页与企业 IM　·　绿色已有 / 蓝色扩展 / 橙色虚线拟建　·　同号端口跨区连接',base+'fillColor=#163141;strokeColor=none;fontColor=#ffffff;spacing=24;fontSize=21;',(0,0,WIDTH,141))
    for k,g in group_map.items():
        owned=list(dict.fromkeys([*g['sources'],*[v['sheet'] for key in g['cards'] for v in notes[key]]]))
        prior='\n'.join('\n'.join(sources[src]['lines']) for src in owned)
        vertex('g-'+k,'<b style="font-size:28px">'+escape(g['title'])+'</b><br>'+escape(g['owner']),base+f'fillColor=#ffffff;strokeColor={PALETTES[k]};strokeWidth=2;spacingLeft=20;spacingTop=7;fontSize=16;fontColor={PALETTES[k]};container=1;collapsible=0;connectable=1;',g['box'], tip=prior)
    for key,c in card_map.items():
        x,y,w,h=c['box'];gx,gy,*_=group_map[c['group']]['box'];bg,col,label=TINTS[c['status']]
        tip='\n'.join([*c['lines'],*port_notes(key),*[l for v in notes[key] for l in v['lines']]])
        details=('<br><span style="font-size:12px">'+escape(' · '.join(badges[key]))+'</span>') if badges[key] else ''
        body=f'<b style="font-size:20px">{escape(c["title"])}</b> <span style="font-size:12px;color:{col}">{label}</span><br>'+'<br>'.join(map(escape,c['lines']))+details
        vertex('n-'+key,body,base+f'fillColor=#fbfcfd;strokeColor={col};strokeWidth=1.5;dashed={int(c["status"]=="new")};fontSize=16;spacing=8;',
               (x-gx,y-gy,w,h),'g-'+c['group'],tip)
    for i,e in enumerate(segments):
        if 'pts' not in e:continue
        outer=e.get('outer'); ab=('g-'+outer[0],'g-'+outer[1]) if outer else ('n-'+e['a'],'n-'+e['b'])
        boxes=(group_map[outer[0]]['box'],group_map[outer[1]]['box']) if outer else (card_map[e['a']]['box'],card_map[e['b']]['box'])
        style=f'edgeStyle=none;endArrow=block;endSize=6;strokeColor={EDGE[e["kind"]]};strokeWidth={2.6 if outer else 1.5};dashed={int(e["kind"]=="event")};'
        for prefix,p,box in zip(('exit','entry'),(e['pts'][0],e['pts'][-1]),boxes):
            x,y,w,h=box;style+=f'{prefix}X={(p[0]-x)/w:.6f};{prefix}Y={(p[1]-y)/h:.6f};{prefix}Perimeter=0;'
        container=ET.SubElement(tree,'object',id=f'e-{i}',label='',tooltip=e['label'])
        c=ET.SubElement(container,'mxCell',parent='1',source=ab[0],target=ab[1],edge='1',style=style)
        geo=ET.SubElement(c,'mxGeometry',relative='1',attrib={'as':'geometry'})
        points=ET.SubElement(geo,'Array',attrib={'as':'points'})
        for x,y in e['pts'][1:-1]:ET.SubElement(points,'mxPoint',x=n(x),y=n(y))
        if 'labelxy' in e:
            x,y=e['labelxy'];w=text_width(e['label'],16)+18
            rotation='rotation=-90;' if e['a']=='notify' and e['b']=='projection' else ''
            vertex(f'label-{i}',escape(e['label']),'text;html=1;align=center;verticalAlign=middle;fillColor=#f7f9fa;strokeColor=none;fontSize=16;fontStyle=1;'+rotation, (x-w/2,y-16,w,25))
    vertex('research','<b>架构取舍的调研来源</b>　'+'　 /　'.join(escape(a+' · '+b) for a,b,_ in research),base+'fillColor=none;strokeColor=none;fontSize=19;', (49,3460,2663,122),tip='\n'.join('\n'.join(sources[k]['lines']) for k in ('references-1','references-2','references-3')))
    vertex('commercial','商业产品行为对照：ChatGPT Work、Claude Cowork、Manus、Kimi Agent、Meta Muse　　　桌面 Web 设计对照：ChatGPT、Claude、Gemini',base+'fillColor=none;strokeColor=none;fontSize=17;',(49,3595,2663,35))
    ET.indent(root);return ET.tostring(root,encoding='utf-8',xml_declaration=True)
def main():
    reachable = {k for g in group_map.values() for k in g['sources']}
    reachable.update(n['sheet'] for entries in notes.values() for n in entries)
    reachable.update(k for _,_,k in research)
    assert reachable == set(sources), ('Unassigned earlier diagram', set(sources)-reachable)
    prior_book=ET.parse(LEGACY/'alliance-architecture.drawio').getroot()
    assert all(page.get('id') in prior_components for page in prior_book)
    svg=render_svg()
    (OUT/'enterprise-platform.svg').write_text(svg)
    (OUT/'enterprise-platform.drawio').write_bytes(render_drawio())
    payload={'cards':card_map,'groups':group_map,'notes':notes,'sources':sources,
             'edges':[{k:v for k,v in e.items() if k in ('a','b','kind','label','code')} for e in segments],
             'research':research,'width':WIDTH,'height':HEIGHT}
    data=json.dumps(payload,ensure_ascii=False,separators=(',',':')).replace('<','\\u003c')
    template=(HERE/'viewer.template.html').read_text()
    (OUT/'enterprise-platform.html').write_text(template.replace('<!-- CANVAS -->',svg).replace('/* PAYLOAD */',data))
    print(f'{len(modules)} domains; {len(card_map)} components; {len(segments)} interfaces; {len(sources)} prior diagrams')
if __name__=='__main__':main()
