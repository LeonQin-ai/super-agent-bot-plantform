package feishu
import("bytes";"crypto/aes";"crypto/cipher";"crypto/sha256";"crypto/subtle";"encoding/base64";"encoding/hex";"encoding/json";"errors";"io";"net/http";"strings";"github.com/LeonQin-ai/super-agent-bot-plantform/control/internal/store")
type Config struct{Token,EncryptKey string;Allowed map[string]struct{}}
type Hook struct{s store.Store;c Config}
func New(s store.Store,c Config)http.Handler{return &Hook{s:s,c:c}}
type event struct{Type string `json:"type"`;Token string `json:"token"`;Challenge string `json:"challenge"`;Encrypt string `json:"encrypt"`;Header struct{ID string `json:"event_id"`;Type string `json:"event_type"`} `json:"header"`;Event struct{Message struct{ChatID string `json:"chat_id"`;ChatType string `json:"chat_type"`;Content string `json:"content"`} `json:"message"`;Sender struct{ID struct{OpenID string `json:"open_id"`} `json:"sender_id"`} `json:"sender"`} `json:"event"`}
func(h *Hook)ServeHTTP(w http.ResponseWriter,r *http.Request){
 if h.c.Token==""||h.c.EncryptKey==""||len(h.c.Allowed)==0{http.Error(w,"Feishu disabled: configure token, encrypt key and allowlist",503);return}
 b,e:=io.ReadAll(http.MaxBytesReader(w,r.Body,1<<20));if e!=nil{http.Error(w,"invalid body",413);return};var v event;if json.Unmarshal(b,&v)!=nil{http.Error(w,"invalid JSON",400);return}
 if v.Type=="url_verification"{if !eq(v.Token,h.c.Token){http.Error(w,"unauthorized",401);return};out(w,200,map[string]string{"challenge":v.Challenge});return}
 if !signature(r,b,h.c.EncryptKey){http.Error(w,"invalid signature",401);return}
 if v.Encrypt!=""{p,e:=decrypt(h.c.EncryptKey,v.Encrypt);if e!=nil||json.Unmarshal(p,&v)!=nil{http.Error(w,"decrypt failed",400);return}}
 if !eq(v.Token,h.c.Token){http.Error(w,"invalid token",401);return};if v.Header.Type!="im.message.receive_v1"{out(w,200,map[string]int{"code":0});return}
 open:=v.Event.Sender.ID.OpenID;if _,ok:=h.c.Allowed[open];!ok{http.Error(w,"sender forbidden",403);return};if v.Event.Message.ChatType!="p2p"{http.Error(w,"direct messages only",403);return}
 var msg struct{Text string `json:"text"`};if json.Unmarshal([]byte(v.Event.Message.Content),&msg)!=nil||strings.TrimSpace(msg.Text)==""{http.Error(w,"text message required",400);return}
 if _,_,e=h.s.IngestFeishuEvent(r.Context(),v.Header.ID,open,v.Event.Message.ChatID,strings.TrimSpace(msg.Text));e!=nil{http.Error(w,"persist event failed",500);return};out(w,200,map[string]int{"code":0})
}
func signature(r *http.Request,b []byte,key string)bool{t,n,s:=r.Header.Get("X-Lark-Request-Timestamp"),r.Header.Get("X-Lark-Request-Nonce"),strings.ToLower(r.Header.Get("X-Lark-Signature"));if t==""||n==""||s==""{return false};x:=sha256.Sum256([]byte(t+n+key+string(b)));return eq(s,hex.EncodeToString(x[:]))}
func decrypt(k,v string)([]byte,error){b,e:=base64.StdEncoding.DecodeString(v);if e!=nil{return nil,e};key:=sha256.Sum256([]byte(k));block,e:=aes.NewCipher(key[:]);if e!=nil{return nil,e};if len(b)==0||len(b)%aes.BlockSize!=0{return nil,errors.New("invalid ciphertext")};p:=make([]byte,len(b));cipher.NewCBCDecrypter(block,key[:aes.BlockSize]).CryptBlocks(p,b);n:=int(p[len(p)-1]);if n<1||n>aes.BlockSize||n>len(p){return nil,errors.New("bad padding")};for _,c:=range p[len(p)-n:]{if int(c)!=n{return nil,errors.New("bad padding")}};return bytes.Clone(p[:len(p)-n]),nil}
func eq(a,b string)bool{if len(a)!=len(b){return false};return subtle.ConstantTimeCompare([]byte(a),[]byte(b))==1}
func out(w http.ResponseWriter,s int,v any){w.Header().Set("Content-Type","application/json");w.WriteHeader(s);_=json.NewEncoder(w).Encode(v)}
