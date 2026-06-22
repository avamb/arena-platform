<?php 
    require("header.php")
?>

<div class="container">
    <p class="description">
        <b>User agreement - a public offer for the sale of tickets for cultural and entertainment events <span class="AgentName"></span></b>

        <BR><BR><BR>
        <b>1.	Introduction</b>
        <BR>
        1.1. On this page you need to place your user agreement.
        <BR><BR>
        <BR>
        
        </p>
</div>
   <script>
        function propsLoaded(){
            let AgentNames = document.querySelectorAll(".AgentName");
            for(let i in AgentNames){
                AgentNames[i].innerHTML = properties.AgentName;
            }
            
            let AgentEmails = document.querySelectorAll(".AgentEmail");
            for(let i in AgentEmails){
                AgentEmails[i].innerHTML = properties.AgentEmail;
            }
            
            document.querySelector(".AgentTIN").innerHTML = properties.AgentTIN;
            document.querySelector(".АgentAddress").innerHTML = properties.АgentAddress;
        }
   </script>
<?php 
    require("footer.php")
?>    
